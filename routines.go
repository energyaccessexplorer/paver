package main

import (
	"fmt"
	"net/http"
)

func routine_admin_boundaries(r *http.Request, in filename, idfield string) (bool, error) {
	rprj, err := vectors_reproject(in)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- *reprojected", rprj), r)

	ids, err := raster_ids(rprj, idfield)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- raster ids", ids), r)

	stripped, err := vectors_strip(rprj, []string{idfield})
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- *stripped", stripped), r)

	socketwrite(fmt.Sprintf("dataset info:\n%s", info(stripped)), r)

	socketwrite("clean up", r)
	trash(rprj)

	if run_server {
		keeps := []filename{ids, stripped}

		for _, f := range keeps {
			socketwrite(fmt.Sprintf("%s -> S3", f), r)
			s3put(f, true)
		}
	}

	socketwrite("done!", r)

	return true, nil
}

func routine_clip_proximity(r *http.Request, in filename, ref filename, fields []string) (bool, error) {
	stripped, err := vectors_strip(in, fields)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- stripped", stripped), r)

	refprj, err := vectors_reproject(ref)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- reprojected reference", refprj), r)

	zeros, err := raster_zeros(refprj)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- zeros", zeros), r)

	clipped, err := vectors_clip(stripped, ref)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- *clipped", clipped), r)

	rstr, err := raster_geometry(clipped, zeros)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- rasterised <- zeros", rstr), r)

	prox, err := raster_proximity(rstr)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- *proximity", prox), r)

	socketwrite("clean up", r)
	trash(in, ref, zeros, stripped, rstr, refprj)

	if run_server {
		keeps := []filename{prox, clipped}

		for _, f := range keeps {
			socketwrite(fmt.Sprintf("%s -> S3", f), r)
			s3put(f, true)
		}
	}

	socketwrite("done!", r)

	return true, nil
}
