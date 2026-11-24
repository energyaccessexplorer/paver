package main

// The crops paver published rasters with before raster_crop put them on the
// outline raster's grid, as they were in paver's history. Only `paver infer`
// runs them: a published raster is rebuilt with the crop that made it, which is
// what tells whether the parameters infer settles on are the ones it was made
// with. Nothing is published with them.

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/energyaccessexplorer/gdal"
)

// The crop's layouts, by when paver made them: one band of the category's type
// (2021-07 to 2026-09, paver 91754a1), then near/sum/average Float32 bands
// (EAE-500, to 2026-11, paver d1d2c56). Both size the output as the outline
// raster but span the outline vectors' extent, so cells are not square.
const (
	crop_raster_2021 = "crop-raster@2021"
	crop_raster_2026 = "crop-raster@2026-09"
)

func routine_crop_raster_2021(w reporter, p routine_params) (string, error) {
	return crop_history_routine(w, p, func(in filename) (filename, error) {
		return raster_crop_2021(in, p.Base, p.Reference, p.Config, p.Resolution)
	})
}

func routine_crop_raster_2026(w reporter, p routine_params) (string, error) {
	return crop_history_routine(w, p, func(in filename) (filename, error) {
		return raster_crop_2026(in, p.Base, p.Reference, p.Config)
	})
}

func crop_history_routine(w reporter, p routine_params, crop func(filename) (filename, error)) (string, error) {
	in := p.Dataset
	in = maybe_zip(in)
	in = maybe_shp(in)

	cropped, err := crop(in)
	if err != nil {
		return "", err
	}

	urls, err := cleanup([]filename{cropped}, []filename{}, w, p.S3)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(`{ "raster": "%s" }`, urls[cropped]), nil
}

func raster_crop_2021(in, base, ref filename, rc raster_config, res int) (filename, error) {
	r, err := gdal.OpenEx(base, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer r.Close()

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	layer, err := crop_history_layer(ref)
	if err != nil {
		return "", err
	}

	resampled := _filename()
	defer trash(resampled)

	release := capture()
	rs, err := gdal.Warp(resampled, nil, []gdal.Dataset{src}, []string{
		"-of", "GTiff",
		"-t_srs", "EPSG:3857",
		"-tr", strconv.Itoa(res), strconv.Itoa(res),
		"-r", rc.Resample,
		"-wo", "NUM_THREADS=ALL_CPUS",
	})
	if result := release(); err != nil {
		return "", errors.New(result)
	}
	defer rs.Close()

	out := _filename()

	release = capture()
	dest, err := gdal.Warp(out, nil, []gdal.Dataset{rs}, []string{
		"-cutline", ref,
		"-crop_to_cutline",
		"-cl", layer,
		"-of", "GTiff",
		"-ts", strconv.Itoa(r.RasterXSize()), strconv.Itoa(r.RasterYSize()),
		"-t_srs", "EPSG:3857",
		"-ot", rc.Numbertype,
		"-dstnodata", strconv.Itoa(rc.Nodata),
		"-wo", "NUM_THREADS=ALL_CPUS",
		"-co", "COMPRESS=DEFLATE",
		"-co", "PREDICTOR=1",
		"-co", "ZLEVEL=9",
	})
	if result := release(); err != nil {
		trash(out)
		return "", errors.New(result)
	}
	dest.Close()

	return out, nil
}

func raster_crop_2026(in, base, ref filename, rc raster_config) (filename, error) {
	r, err := gdal.OpenEx(base, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	x, y := r.RasterXSize(), r.RasterYSize()
	r.Close()

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	layer, err := crop_history_layer(ref)
	if err != nil {
		return "", err
	}

	gtiff := []string{"-co", "COMPRESS=DEFLATE", "-co", "PREDICTOR=1", "-co", "ZLEVEL=9"}

	var bands []filename
	defer func() { trash(bands...) }()

	for _, method := range []string{rc.Resample, "sum", "average"} {
		band := _filename()
		bands = append(bands, band)

		release := capture()
		dest, err := gdal.Warp(band, nil, []gdal.Dataset{src}, append([]string{
			"-of", "GTiff",
			"-t_srs", "EPSG:3857",
			"-ts", strconv.Itoa(x), strconv.Itoa(y),
			"-r", method,
			"-ot", "Float32",
			"-dstnodata", strconv.Itoa(rc.Nodata),
			"-cutline", ref,
			"-crop_to_cutline",
			"-cl", layer,
			"-wo", "NUM_THREADS=ALL_CPUS",
		}, gtiff...))
		if result := release(); err != nil {
			return "", fmt.Errorf("warp (%s): %s", method, result)
		}
		dest.Close()
	}

	vrt := _filename() + ".vrt"
	defer trash(vrt)

	if buf, err := exec.Command("gdalbuildvrt", append([]string{"-separate", vrt}, bands...)...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("gdalbuildvrt: %v: %s", err, buf)
	}

	out := _filename()

	if buf, err := exec.Command("gdal_translate", append(append([]string{"-of", "GTiff"}, gtiff...), vrt, out)...).CombinedOutput(); err != nil {
		trash(out)
		return "", fmt.Errorf("gdal_translate: %v: %s", err, buf)
	}

	return out, nil
}

func crop_history_layer(ref filename) (string, error) {
	f := gdal.OpenDataSource(ref, 0)
	defer f.Destroy()

	if f.LayerCount() == 0 {
		return "", errors.New("the outline vectors have no layer")
	}

	return f.LayerByIndex(0).Name(), nil
}

// Before paver ceace15 (2022-09) clip-proximity clipped the source stripped to
// the fields admin selected -- none when none were -- against the outline as
// it is; it has clipped the whole source against a simplified outline since.
const clip_proximity_2021 = "clip-proximity@2021"

func routine_clip_proximity_2021(w reporter, p routine_params) (string, error) {
	in := p.Dataset
	in = maybe_zip(in)
	in = maybe_shp(in)

	stripped := _filename()
	defer trash(stripped)

	if buf, err := exec.Command("ogr2ogr", "-f", "GeoJSON", "-select", strings.Join(p.Fields, ","), stripped, in).CombinedOutput(); err != nil {
		return "", fmt.Errorf("ogr2ogr -select: %v: %s", err, buf)
	}

	refprj, err := vectors_reproject(p.Reference, 3857, w)
	if err != nil {
		return "", err
	}

	zeros, err := raster_zeros(refprj, p.Resolution, w)
	if err != nil {
		return "", err
	}

	clipped, err := vectors_clip(stripped, p.Reference, w)
	if err != nil {
		return "", err
	}

	rstr, err := raster_geometry_ones(clipped, zeros, w)
	if err != nil {
		return "", err
	}

	prox, err := raster_proximity(rstr, w)
	if err != nil {
		return "", err
	}

	urls, err := cleanup([]filename{clipped, prox}, []filename{rstr, refprj}, w, p.S3)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(`{ "vectors": "%s", "raster": "%s" }`, urls[clipped], urls[prox]), nil
}
