-- The job list for `paver repave`: one JSON line per published raster.
--
--   psql -At -f repave.sql > jobs.jsonl
--
-- A dataset whose crop-raster parameters made its published raster -- paved
-- with them by admin, or checked against it by `paver infer` -- gets a
-- crop-raster job, with what payload_fill sends: the source, the geography's
-- outline vectors and raster, and the recorded config and resolution -- what
-- made the file, which the category's config need not be any more. Any other
-- raster is only re-encoded, which keeps every cell: with no known parameters,
-- rebuilding it would guess. "root" is the country, the top of the geography's tree. Filter
-- on "geography" or "root" to repave a part at a time.

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
		and d.paver_params->>'routine' = 'crop-raster'
		and d.paver_params->'check'->>'status' in ('reproduced', 'equivalent', 'matched', 'paved')
		and o.referenceurl is not null
		and o.baseurl is not null
	then jsonb_build_object(
		'endpoint', p->>'endpoint',
		'dataset', d.id,
		'geography', d.geography_id,
		'root', t.path[1],
		'routine', 'crop-raster',
		'dataseturl', src.endpoint,
		'referenceurl', o.referenceurl,
		'baseurl', o.baseurl,
		'config', d.paver_params->'params'->'config',
		'resolution', (d.paver_params->'params'->>'resolution')::int)
	else jsonb_build_object(
		'endpoint', p->>'endpoint',
		'dataset', d.id,
		'geography', d.geography_id,
		'root', t.path[1])
	end
from datasets d
join outline o on o.geography_id = d.geography_id
join geographies_tree_up t on t.id = d.geography_id
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
