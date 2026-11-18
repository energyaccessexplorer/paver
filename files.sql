-- The copy list for `paver repave -move`: every file a dataset names, in
-- processed_files or source_files, one JSON line per file and country.
--
--   psql -At -f files.sql > copies.jsonl
--
-- Admin clones a dataset into each subgeography with the same files, so a
-- file is copied once per country ("root"), under the highest geography in
-- that country that names it; every dataset in the country then shares the
-- copy. "column" is processed when any dataset there names it as a
-- processed file, source otherwise.

with files as (
	select f->>'endpoint' as endpoint, d.geography_id, t.path, c.col
	from datasets d
	join geographies_tree_up t on t.id = d.geography_id
	cross join lateral (values
		('processed', coalesce(d.processed_files, '[]')),
		('source', coalesce(d.source_files, '[]'))
	) c(col, files)
	cross join lateral jsonb_array_elements(c.files) f
	where f->>'endpoint' like 'http%'
)
select distinct on (endpoint, path[1]) jsonb_build_object(
	'endpoint', endpoint,
	'geography', geography_id,
	'root', path[1],
	'routine', 'copy',
	'column', case when bool_or(col = 'processed') over (partition by endpoint, path[1]) then 'processed' else 'source' end)
from files
order by endpoint, path[1], cardinality(path), geography_id;
