-- The job list for `paver infer`: one JSON line per dataset that publishes
-- paver outputs, with what its routine needs to be run again.
--
--   psql -At -f params.sql > jobs.jsonl
--
-- The routine is the one admin's paving dialog picks for the dataset's type
-- (admin/src/paver.js, routine()): a raster made from a raster source is
-- crop-raster -- also when the category carries a csv legend, because the
-- legend is not what the raster is made from; only a raster without a raster
-- source is burned from a csv. The source, the geography's outline vectors and
-- raster, the geography's resolution, the category's raster->paver config and
-- vectors_configuration are what payload_fill sends; "outputs" is what the
-- dataset publishes, by function, and "existing" the parameters it records
-- already. "root" is the country, the top of the geography's tree.
--
-- A dataset with no source for its routine is left out: nothing can be run for
-- it. Filter on "geography" or "root" to infer a part at a time.

with outline as (
	select
		g.id as geography_id,
		g.resolution,
		(select f->>'endpoint' from jsonb_array_elements(o.processed_files) f where f->>'func' = 'vectors' limit 1) as referenceurl,
		(select f->>'endpoint' from jsonb_array_elements(o.processed_files) f where f->>'func' = 'raster' limit 1) as baseurl
	from geographies g
	left join datasets o on o.id::text = g.configuration->'divisions'->0->>'dataset_id'
),
sources as (
	select
		d.id,
		(select f->>'endpoint' from jsonb_array_elements(coalesce(d.source_files, '[]')) f where f->>'func' = 'raster' limit 1) as raster,
		(select f->>'endpoint' from jsonb_array_elements(coalesce(d.source_files, '[]')) f where f->>'func' = 'csv' limit 1) as csv,
		(select f->>'endpoint' from jsonb_array_elements(coalesce(d.source_files, '[]')) f where f->>'func' = 'vectors' limit 1) as vectors
	from datasets d
),
published as (
	select d.id, jsonb_object_agg(p->>'func', p->>'endpoint') as outputs
	from datasets d
	cross join lateral jsonb_array_elements(coalesce(d.processed_files, '[]')) p
	where p->>'endpoint' like 'http%'
	group by d.id
),
routed as (
	select
		d.id,
		d.geography_id,
		type(d) as type,
		s.raster,
		s.csv,
		s.vectors,
		o.resolution,
		o.referenceurl,
		o.baseurl,
		p.outputs,
		c.raster->'paver' as config,
		(c.vectors->'paver'->>'simplify')::float4 as category_simplify,
		d.vectors_configuration,
		d.paver_params,
		case
			when type(d) in ('raster-valued', 'raster') and s.raster is not null then 'crop-raster'
			when type(d) = 'raster' and s.csv is not null then 'csv-raster'
			when type(d) = 'polygons-timeline' then 'simplify'
			when type(d) = 'polygons-boundaries' then 'admin-boundaries'
			when type(d) = 'points' and s.csv is not null then 'csv-points'
			when type(d) in ('points', 'lines', 'polygons') then 'clip-proximity'
		end as routine
	from datasets d
	join categories c on c.id = d.category_id
	join sources s on s.id = d.id
	join outline o on o.geography_id = d.geography_id
	join published p on p.id = d.id
	where p.outputs ?| array['raster', 'vectors']
)
select jsonb_strip_nulls(jsonb_build_object(
	'dataset', r.id,
	'geography', r.geography_id,
	'root', t.path[1],
	'type', r.type,
	'routine', r.routine,
	'dataseturl', case
		when r.routine in ('csv-raster', 'csv-points') then r.csv
		when r.routine = 'crop-raster' then r.raster
		else r.vectors
	end,
	'referenceurl', r.referenceurl,
	'baseurl', r.baseurl,
	'resolution', r.resolution,
	'category_simplify', r.category_simplify,
	'config', r.config,
	'vectors_configuration', r.vectors_configuration,
	'outputs', r.outputs,
	'existing', r.paver_params))
from routed r
join geographies_tree_up t on t.id = r.geography_id
where r.routine is not null
	and (case r.routine
		when 'csv-raster' then r.csv
		when 'csv-points' then r.csv
		when 'crop-raster' then r.raster
		else r.vectors
	end) is not null
	and (r.routine <> 'crop-raster' or (
		r.config->>'resample' is not null
		and r.referenceurl is not null
		and r.baseurl is not null))
order by r.geography_id, r.id;
