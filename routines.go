package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

// cleanup publishes keeps and answers each one's URL.
func cleanup(keeps []filename, deletes []filename, w reporter, s3 s3config) (map[filename]string, error) {
	w("CLEAN UP")

	trash(deletes...)

	urls := map[filename]string{}

	// Without a bucket nothing is published: infer keeps the outputs where
	// they are to compare them with what was.
	if s3.Name == "" {
		for _, f := range keeps {
			urls[f] = s3endpoint(s3, object_key(f))
		}
		return urls, nil
	}

	for _, f := range keeps {
		if is_geotiff(f) {
			if err := raster_deliver(f, w); err != nil {
				return nil, err
			}
		}

		key := object_key(f)

		w("%s -> S3", f)
		if !s3put_as(f, key, s3) {
			return nil, errors.New("s3 upload failed: " + f)
		}
		trash(f)

		urls[f] = s3endpoint(s3, key)
	}

	w("DONE")

	return urls, nil
}

func routine_admin_boundaries(w reporter, p routine_params) (string, error) {
	in := p.Dataset
	in = maybe_zip(in)
	in = maybe_shp(in)

	rprj, err := vectors_reproject(in, 3857, w)
	if err != nil {
		return "", err
	}
	w("%s <- reprojected", rprj)

	ids, err := raster_ids(rprj, p.Attr, p.Resolution, w)
	if err != nil {
		return "", err
	}
	w("%s <- *raster ids", ids)

	stripped, err := vectors_strip(rprj, []string{p.Attr}, w)
	if err != nil {
		return "", err
	}
	w("%s <- stripped", stripped)

	rprjstripped, err := vectors_reproject(in, 4326, w)
	if err != nil {
		return "", err
	}
	w("%s <- *stripped reprojected", rprjstripped)

	info := vectors_info(rprjstripped)

	urls, err := cleanup(
		[]filename{ids, rprjstripped},
		[]filename{rprj, stripped},
		w, p.S3,
	)
	if err != nil {
		return "", err
	}

	jinfo, err := json.Marshal(info)
	if err != nil {
		return "", err
	}

	jsonstr := fmt.Sprintf(
		`{ "vectors": "%s", "raster": "%s", "info": %s }`,
		urls[rprjstripped],
		urls[ids],
		jinfo,
	)

	return jsonstr, nil
}

func routine_simplify(w reporter, p routine_params) (string, error) {
	in := p.Dataset
	in = maybe_zip(in)
	in = maybe_shp(in)

	simpl, err := vectors_simplify(in, p.Simplify, w)
	if err != nil {
		return "", err
	}
	w("%s <- *simplified", simpl)

	prj, err := vectors_reproject(simpl, 3857, w)
	if err != nil {
		return "", err
	}
	w("%s <- reprojected", prj)

	ids, err := raster_ids(prj, p.Attr, p.Resolution, w)
	if err != nil {
		return "", err
	}
	w("%s <- *raster ids", ids)

	urls, err := cleanup(
		[]filename{simpl, ids},
		[]filename{},
		w, p.S3,
	)
	if err != nil {
		return "", err
	}

	jsonstr := fmt.Sprintf(`{ "vectors": "%s", "raster": "%s" }`, urls[simpl], urls[ids])

	return jsonstr, nil
}

func routine_clip_proximity(w reporter, p routine_params) (string, error) {
	in := p.Dataset
	in = maybe_zip(in)
	in = maybe_shp(in)

	stripped, err := vectors_strip(in, p.Fields, w)
	if err != nil {
		return "", err
	}
	w("%s <- stripped", stripped)

	refprj, err := vectors_reproject(p.Reference, 3857, w)
	if err != nil {
		return "", err
	}
	w("%s <- reprojected reference", refprj)

	zeros, err := raster_zeros(refprj, p.Resolution, w)
	if err != nil {
		return "", err
	}
	w("%s <- zeros", zeros)

	simpl, err := vectors_simplify(p.Reference, p.Simplify, w)
	if err != nil {
		return "", err
	}
	w("%s <- simplified reference", simpl)

	clipped, err := vectors_clip(in, simpl, w)
	if err != nil {
		return "", err
	}
	w("%s <- *clipped", clipped)

	last := clipped

	if p.Dissolve {
		dissolved, err := vectors_dissolve(clipped, p.Fields, w)
		if err != nil {
			return "", err
		}
		w("%s <- *dissolved", dissolved)

		last = dissolved

		trash(clipped)
	}

	rstr, err := raster_geometry_ones(last, zeros, w)
	if err != nil {
		return "", err
	}
	w("%s <- rasterised <- zeros", rstr) // overwrites zeros

	prox, err := raster_proximity(rstr, w)
	if err != nil {
		return "", err
	}
	w("%s <- *proximity", prox)

	urls, err := cleanup(
		[]filename{last, prox},
		[]filename{stripped, rstr, refprj, simpl},
		w, p.S3,
	)
	if err != nil {
		return "", err
	}

	jsonstr := fmt.Sprintf(`{ "vectors": "%s", "raster": "%s" }`, urls[last], urls[prox])

	return jsonstr, nil
}

func routine_csv_points(w reporter, p routine_params) (string, error) {
	points, err := csv_points(p.Dataset, p.LngLat, p.Fields)
	if err != nil {
		return "", err
	}
	w("%s <- csv points", points)

	refprj, err := vectors_reproject(p.Reference, 3857, w)
	if err != nil {
		return "", err
	}
	w("%s <- reprojected reference", refprj)

	zeros, err := raster_zeros(refprj, p.Resolution, w)
	if err != nil {
		return "", err
	}
	w("%s <- zeros", zeros)

	clipped, err := vectors_clip(points, p.Reference, w)
	if err != nil {
		return "", err
	}
	w("%s <- *clipped", clipped)

	rstr, err := raster_geometry_ones(clipped, zeros, w)
	if err != nil {
		return "", err
	}
	w("%s <- rasterised <- zeros", rstr) // overwrites zeros

	prox, err := raster_proximity(rstr, w)
	if err != nil {
		return "", err
	}
	w("%s <- *proximity", prox)

	urls, err := cleanup(
		[]filename{clipped, prox},
		[]filename{points, rstr, refprj},
		w, p.S3,
	)
	if err != nil {
		return "", err
	}

	jsonstr := fmt.Sprintf(`{ "vectors": "%s", "raster": "%s" }`, urls[clipped], urls[prox])

	return jsonstr, nil
}

func crop_raster_output(w reporter, p routine_params) (filename, error) {
	in := p.Dataset
	in = maybe_zip(in)
	in = maybe_shp(in)

	return raster_crop(in, p.Base, p.Reference, p.Config, w)
}

func routine_crop_raster(w reporter, p routine_params) (string, error) {
	cropped, err := crop_raster_output(w, p)
	if err != nil {
		return "", err
	}
	w("%s <- cropped", cropped)

	urls, err := cleanup(
		[]filename{cropped},
		[]filename{},
		w, p.S3,
	)
	if err != nil {
		return "", err
	}

	jsonstr := fmt.Sprintf(`{ "raster": "%s" }`, urls[cropped])

	return jsonstr, nil
}

func routine_subgeographies(w reporter, p routine_params) (string, error) {
	in := p.Dataset
	in = maybe_zip(in)
	in = maybe_shp(in)

	r, _ := vectors_features_split(in, p.Attr, w)

	for i, f := range r {
		urls, err := cleanup([]filename{f}, []filename{}, w, p.S3)
		if err != nil {
			return "", err
		}
		r[i] = urls[f]
	}

	jsonstr, _ := json.Marshal(r)

	return string(jsonstr), nil
}

func routine_vectors_extra_attributes(w reporter, p routine_params) (string, error) {
	in := p.Dataset
	in = maybe_zip(in)
	in = maybe_shp(in)

	r, _ := vectors_features_extra_attrs(in, w)

	for i, f := range r {
		urls, err := cleanup([]filename{f}, []filename{}, w, p.S3)
		if err != nil {
			return "", err
		}
		r[i] = urls[f]
	}

	jsonstr, _ := json.Marshal(r)

	return string(jsonstr), nil
}

func routine_csv_raster(w reporter, p routine_params) (string, error) {
	in := p.Dataset

	points, err := csv_points(in, p.LngLat, []string{p.Attr})
	if err != nil {
		return "", err
	}
	w("%s <- csv points", points)

	refprj, err := vectors_reproject(p.Reference, 3857, w)
	if err != nil {
		return "", err
	}
	w("%s <- reprojected reference", refprj)

	zeros, err := raster_zeros(refprj, p.Resolution, w)
	if err != nil {
		return "", err
	}
	w("%s <- zeros", zeros)

	clipped, err := vectors_clip(points, p.Reference, w)
	if err != nil {
		return "", err
	}
	w("%s <- clipped", clipped)

	rstr, err := raster_geometry_attr(clipped, zeros, p.Attr, w)
	if err != nil {
		return "", err
	}
	w("%s <- rasterised", rstr)

	urls, err := cleanup(
		[]filename{rstr},
		[]filename{points, clipped, refprj},
		w, p.S3,
	)
	if err != nil {
		return "", err
	}

	jsonstr := fmt.Sprintf(`{ "raster": "%s" }`, urls[rstr])

	return jsonstr, nil
}
