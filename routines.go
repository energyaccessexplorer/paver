package main

import (
	"fmt"
	"net/http"
)

func admin_boundaries(in filename, idattr string) (bool, error) {
	ids, err := ids_raster(in, idattr)
	if err != nil {
		return false, err
	}
	println("ids_raster:", ids)

	stripped, err := strip(in, []string{idattr})
	if err != nil {
		return false, err
	}

	println(info(stripped))

	return true, nil
}

func vectors_clipped_routine(r *http.Request, in filename, ref filename, attrs []string) (bool, error) {
	stripped, err := strip(in, attrs)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- stripped", stripped), r)

	refprj, err := reproject(ref)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- reprojected reference", refprj), r)

	zeros, err := zeros_raster(refprj)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- zeros", zeros), r)

	clipped, err := clip(stripped, ref)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- *clipped", clipped), r)

	rstr, err := geometry_raster(clipped, zeros)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- rasterised <- zeros", rstr), r)

	prox, err := proximity_raster(rstr)
	if err != nil {
		return false, err
	}
	socketwrite(fmt.Sprintf("%s <- *proximity", prox), r)

	socketwrite("clean up", r)
	cleanup_files(in, ref, zeros, stripped, rstr, refprj)

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

func vectors_routine(in filename, ref filename, attrs []string) (bool, error) {
	stripped, err := strip(in, attrs)
	if err != nil {
		return false, err
	}
	println("stripped:", stripped)

	zeros, err := zeros_raster(ref)
	if err != nil {
		return false, err
	}
	println("zeros:", zeros)

	rstr, err := geometry_raster(in, zeros)
	if err != nil {
		return false, err
	}
	println("rasterised:", rstr)

	prox, err := proximity_raster(rstr)
	if err != nil {
		return false, err
	}
	println("proximity_raster:", prox)

	cleanup_files(zeros, rstr)

	return true, nil
}
