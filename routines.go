package main

import (
	"encoding/json"
	"fmt"
)

func routine_admin_boundaries(w reporter, in filename, idfield string, resolution int) (string, error) {
	rprj, err := vectors_reproject(in, 3857)
	if err != nil {
		return "", err
	}
	w("%s <- reprojected", rprj)

	ids, err := raster_ids(rprj, idfield, resolution)
	if err != nil {
		return "", err
	}
	w("%s <- *raster ids", ids)

	stripped, err := vectors_strip(rprj, []string{idfield})
	if err != nil {
		return "", err
	}
	w("%s <- stripped", stripped)

	rprjstripped, err := vectors_reproject(in, 4326)
	if err != nil {
		return "", err
	}
	w("%s <- *stripped reprojected", rprjstripped)

	info := vectors_info(rprjstripped)

	w("CLEAN UP")

	trash(rprj, stripped)

	if run_server {
		keeps := []filename{ids, rprjstripped}

		for _, f := range keeps {
			w("%s -> S3", f)
			s3put(f)
			trash(f)
		}
	}

	w("DONE")

	jinfo, err := json.Marshal(info)
	if err != nil {
		fmt.Println(err)
	}

	jsonstr := fmt.Sprintf(
		`{ "vectors": "%s", "raster": "%s", "info": %s }`,
		_uuid(rprjstripped),
		_uuid(ids),
		jinfo,
	)

	return jsonstr, nil
}

func routine_clip_proximity(w reporter, in filename, ref filename, fields []string, resolution int) (string, error) {
	stripped, err := vectors_strip(in, fields)
	if err != nil {
		return "", err
	}
	w("%s <- stripped", stripped)

	refprj, err := vectors_reproject(ref, 3857)
	if err != nil {
		return "", err
	}
	w("%s <- reprojected reference", refprj)

	zeros, err := raster_zeros(refprj, resolution)
	if err != nil {
		return "", err
	}
	w("%s <- zeros", zeros)

	clipped, err := vectors_clip(stripped, ref, w)
	if err != nil {
		return "", err
	}
	w("%s <- *clipped", clipped)

	rstr, err := raster_geometry(clipped, zeros)
	if err != nil {
		return "", err
	}
	w("%s <- rasterised <- zeros", rstr) // overwrites zeros

	prox, err := raster_proximity(rstr)
	if err != nil {
		return "", err
	}
	w("%s <- *proximity", prox)

	w("CLEAN UP")
	trash(in, ref, stripped, rstr, refprj)

	if run_server {
		keeps := []filename{clipped, prox}

		for _, f := range keeps {
			w("%s -> S3", f)
			s3put(f)
			trash(f)
		}
	}

	w("DONE")

	jsonstr := fmt.Sprintf(`{ "vectors": "%s", "raster": "%s" }`, _uuid(clipped), _uuid(prox))

	return jsonstr, nil
}

func routine_crop_raster(w reporter, in filename, base filename, ref filename, conf string, resolution int) (string, error) {
	var c raster_config
	err := json.Unmarshal([]byte(conf), &c)
	if err != nil {
		return "", err
	}

	cropped, err := raster_crop(in, base, ref, c, resolution, w)
	if err != nil {
		return "", err
	}
	w("%s <- cropped", cropped)

	w("CLEAN UP")

	if run_server {
		keeps := []filename{cropped}

		for _, f := range keeps {
			w("%s -> S3", f)
			s3put(f)
			trash(f)
		}
	}

	w("DONE")

	jsonstr := fmt.Sprintf(`{ "raster": "%s" }`, _uuid(cropped))

	return jsonstr, nil
}

func routine_subgeographies(w reporter, in filename, id string) (string, error) {
	r, _ := vectors_features_split(in, id, w)

	w("CLEAN UP")

	if run_server {
		for i, f := range r {
			r[i] = _uuid(r[i])

			w("%s -> S3", f)
			s3put(f)
			trash(f)
		}
	}

	w("DONE")

	jsonstr, _ := json.Marshal(r)

	return string(jsonstr), nil
}
