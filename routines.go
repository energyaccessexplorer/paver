package main

func admin_boundaries(in filename, idattr string) {
	ids, err := ids_raster(in, idattr)
	if err != nil {
		panic(err)
	}
	println("ids_raster:", ids)

	stripped, err := strip(in, []string{idattr})
	if err != nil {
		panic(err)
	}

	println(info(stripped))
}

func vectors_clipped_routine(in filename, tg filename, ref filename, idattrs []string) {
	stripped, err := strip(in, idattrs)
	if err != nil {
		panic(err)
	}
	println("stripped:", stripped)

	zeros, err := zeros_raster(ref)
	if err != nil {
		panic(err)
	}
	println("zeros:", zeros)

	clipped, err := clip(in, tg)
	if err != nil {
		panic(err)
	}
	println("clipped:", clipped)

	rstr, err := geometry_raster(clipped, zeros)
	if err != nil {
		panic(err)
	}
	println("rasterised:", rstr)

	prox, err := proximity_raster(rstr)
	if err != nil {
		panic(err)
	}
	println("proximity_raster:", prox)
}
