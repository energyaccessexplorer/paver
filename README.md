# Energy Access Explorer Paver

Paver is the EAE's backend for processing the geolocated data needed for the
EAE's [tool](https://github.com/energyaccessexplorer/tool)


## Building & Hacking

Dependencies:

- Go (> 1.16)
- GDAL (>= 3.3)
- S3-compatible bucket
- JWT authentication

To get started, edit the `.env` to your needs and run

	$ make


## Buckets

`-buckets` names a JSON file mapping a bucket name to where its objects live.
Every entry has `name`, `provider`, `bucket`, `directory` and `region`;
`key`/`secret` are its S3 credentials, and `acl` is read but unused. The
providers are:

- `s3.amazonaws.com` — AWS S3. Objects are published at
  `https://<bucket>.s3.amazonaws.com/<directory>/<key>`.
- `local` — a directory. `directory` is where objects are written, `bucket` the
  URL they are served from.
- `supabase` — Supabase Storage's S3 protocol: `endpoint` is its S3 API base
  (addressed path-style), `public` the base of the public object URL
  (`<bucket>/<directory>/<key>` is appended), and `region` with `key`/`secret`
  the S3 credentials. Presigned PUTs are signed for `endpoint`; `s3has` HEADs
  the public URL, so the bucket must be public.

`"scratch": true` marks a bucket whose contents are disposable: `repave` fills
it without `-apply`, and accepts jobs whose source raster lives anywhere
instead of only in the target bucket.

A bucket for the local Supabase stack takes the S3 credentials `run-supabase`
writes to `data/supabase/keys.env` (pasted in place of the `$…` names below):

```json
{
	"name": "local",
	"provider": "supabase",
	"endpoint": "http://127.0.0.1:54332/s3",
	"public": "http://eae.localhost/storage/v1/object/public",
	"bucket": "geotiff-test",
	"directory": "repave",
	"region": "local",
	"key": "$SUPABASE_S3_ACCESS_KEY_ID",
	"secret": "$SUPABASE_S3_SECRET_ACCESS_KEY",
	"scratch": true
}
```

`paver repave -bucket local -list ... -manifest ...` then writes the re-encoded
rasters to Storage and returns public URLs
(`http://eae.localhost/storage/v1/object/public/geotiff-test/repave/<sha256>`)
for the manifest, instead of the `local` provider's `cache/geotiff-test`
directory. `endpoint` is the direct Storage address: a presigned URL covers the
`Host` header, so it only survives a proxy that passes the original host
through. Point `endpoint` at `http://eae.localhost/storage/v1/s3` when the
presigned URL is to be used through the gateway, provided the gateway keeps the
`Host` header.


## License

This project is licensed under MIT. Additionally, you must read the
[attribution page](https://www.energyaccessexplorer.org/attribution)
before using any part of this project.
