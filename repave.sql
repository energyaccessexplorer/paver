-- The job list for `paver repave`: one JSON line per published raster.
--
--   psql -At -f repave.sql > jobs.jsonl
--
-- A dataset admin paves with crop-raster (admin/src/paver.js, routine():
-- type raster-valued, or raster without a csv source) gets a crop-raster job,
-- with what payload_fill sends: the source, the geography's
-- outline vectors and raster, the category's raster->paver config and the
-- geography's resolution. Any other raster (proximity, ids, csv) is only
-- re-encoded: the parameters it was paved with are not stored. Filter on
-- "geography" to repave one geography at a time.

with outline as (
	select
		g.id as geography_id,
		g.resolution,
		(select f->>'endpoint' from jsonb_array_elements(o.processed_files) f where f->>'func' = 'vectors' limit 1) as referenceurl,
		(select f->>'endpoint' from jsonb_array_elements(o.processed_files) f where f->>'func' = 'raster' limit 1) as baseurl
	from geographies g
	left join datasets o on o.id::text = g.configuration->'divisions'->0->>'dataset_id'
)
select case
	when src.endpoint is not null
		and (type(d) = 'raster-valued'
			or (type(d) = 'raster' and not exists (select 1 from jsonb_array_elements(d.source_files) f where f->>'func' = 'csv')))
		and c.raster->'paver'->>'resample' is not null
		and o.referenceurl is not null
		and o.baseurl is not null
	then jsonb_build_object(
		'endpoint', p->>'endpoint',
		'dataset', d.id,
		'geography', d.geography_id,
		'routine', 'crop-raster',
		'dataseturl', src.endpoint,
		'referenceurl', o.referenceurl,
		'baseurl', o.baseurl,
		'config', c.raster->'paver',
		'resolution', o.resolution)
	else jsonb_build_object(
		'endpoint', p->>'endpoint',
		'dataset', d.id,
		'geography', d.geography_id)
	end
from datasets d
join categories c on c.id = d.category_id
join outline o on o.geography_id = d.geography_id
cross join lateral jsonb_array_elements(d.processed_files) p
left join lateral (
	select f->>'endpoint' as endpoint
	from jsonb_array_elements(d.source_files) f
	where f->>'func' = 'raster'
	limit 1
) src on true
where p->>'func' = 'raster'
	and p->>'endpoint' like 'http%'
order by d.geography_id, d.id;
