package main

// A crop raster's parameters, checked on the published raster's own grid.
//
// Which grid a crop lands on, and which source cell a nearest-neighbour sample
// picks, is decided by the GDAL and PROJ that ran it: production's rasters were
// cropped by three crops in turn, each with the GDAL of its server, and no
// rebuild here reproduces them cell for cell. What the parameters decide --
// the resampling, the nodata, the number type -- is checked instead by warping
// the source onto the published grid, crop by crop as the published layout
// says, and profiling how the rebuild differs from the published band
// (grid_profile):
//
//   - the nodata and the number type are the published raster's;
//   - the recorded resampling, the category's when it passes and otherwise the
//     closest of GDAL's that does, leaves data in one raster only at the
//     outline's edge and on hardly any cell inside it, puts 95% of the cells
//     within grid_deviation of the band's range -- a band of classes, all but
//     grid_class_share in the same class -- and keeps the band's total within
//     grid_drift.
//
// The resampled band of a three-band raster is tried both ways a crop has made
// it: warped onto the grid at once, and -- as the 2021 crop did, and the build
// that re-paved production for EAE-500 kept for that band -- resampled to the
// geography's resolution first. The sum and average bands are measured too,
// but not judged: GDAL has changed how they weigh a partly covered cell. Parameters that pass are "matched":
// validated, but not reproduced.

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/energyaccessexplorer/gdal"
)

const infer_matched = "matched"

const (
	// A share of the band's range: interpolated values follow PROJ's
	// coordinates to the last digit, and those move between versions.
	grid_tolerance = 0.01

	// What a rebuild may differ by, as shares of the cells with data in either:
	// a GDAL that cuts a cell at the outline either way, or samples a cell
	// beside the one production's did, but not other parameters.
	grid_interior_share = 1e-3
	grid_edge_share     = 0.02
	grid_class_share    = 1e-3
	// As a share of the band's range, and of the published total.
	grid_deviation = 0.02
	grid_drift     = 0.01
	// A band of integers taking at most this many values holds classes.
	grid_classes = 32
)

// grid_rules names these criteria in each check: a crop checked by others is
// checked again.
const grid_rules = "grid-2"

var grid_resamplings = []string{"near", "bilinear", "cubic", "cubicspline", "lanczos", "average", "sum", "mode", "max", "min", "med", "q1", "q3"}

const grid_resampled_first = ", resampled to the resolution first"

// published_grid is what a crop onto the published raster has to match.
type published_grid struct {
	gt       [6]float64
	x, y     int
	proj     string
	bands    int
	nodata   float64
	hasnd    bool
	datatype string
}

func (g published_grid) square() bool {
	return math.Abs(math.Abs(g.gt[1])-math.Abs(g.gt[5])) <= 1e-9*math.Abs(g.gt[1])
}

func read_published_grid(f filename) (published_grid, error) {
	ds, err := gdal.Open(f, gdal.ReadOnly)
	if err != nil {
		return published_grid{}, err
	}
	defer ds.Close()

	b := ds.RasterBand(1)
	nd, ok := b.NoDataValue()

	return published_grid{
		gt:       ds.GeoTransform(),
		x:        ds.RasterXSize(),
		y:        ds.RasterYSize(),
		proj:     ds.Projection(),
		bands:    ds.RasterCount(),
		nodata:   nd,
		hasnd:    ok,
		datatype: b.RasterDataType().Name(),
	}, nil
}

// infer_crop_on_grid settles a crop raster's parameters by agreement on the
// published grid. cfg is the category's config.
func infer_crop_on_grid(src, ref, published filename, cfg raster_config, res int) (raster_config, infer_check) {
	check := infer_check{Status: infer_differs, At: infer_now(), Agreement: map[string]float64{}}

	g, err := read_published_grid(published)
	if err != nil {
		check.Status, check.Note = infer_failed, err.Error()
		return cfg, check
	}

	// The 2021 crop wrote a band per source band, of the config's type; the
	// later ones write Float32 near/sum/average bands.
	single := g.bands == 1 || g.datatype != "Float32"
	layout := "near/sum/average bands"
	if single {
		layout = "a band per source band"
	}

	srcbands, err := raster_band_count(src)
	if err != nil {
		check.Status, check.Note = infer_failed, err.Error()
		return cfg, check
	}

	// The bands compared per rebuild: the first of the source's, or all of them.
	per := 1
	switch {
	case single:
		per = min(g.bands, srcbands)
	case g.bands%3 == 0 && g.bands/3 == srcbands:
		per = srcbands
	}

	notes := []string{"on the published grid (" + layout + ")"}

	edge, err := outline_edge(ref, g)
	if err != nil {
		check.Status, check.Note = infer_failed, "the outline's edge: "+err.Error()
		return cfg, check
	}

	chosen := cfg
	if !g.hasnd || float64(cfg.Nodata) != g.nodata {
		if !g.hasnd || g.nodata != math.Trunc(g.nodata) {
			check.Note = fmt.Sprintf("%s: the published nodata %v is not one a config can name", notes[0], g.nodata)
			return cfg, check
		}
		chosen.Nodata = int(g.nodata)
		notes = append(notes, fmt.Sprintf("the published nodata %d, not the category's %d", chosen.Nodata, cfg.Nodata))
	}

	if single && !strings.EqualFold(cfg.Numbertype, g.datatype) {
		chosen.Numbertype = g.datatype
		notes = append(notes, fmt.Sprintf("the published type %s, not the category's %s", g.datatype, cfg.Numbertype))
	}

	rebuild := func(method string) (filename, error) {
		if single {
			return warp_on_grid_2021(src, ref, g, method, chosen, res)
		}
		return warp_on_grid(src, ref, g, method, "Float32", chosen.Nodata)
	}

	// The band-one rebuilds to try, by what the note calls them.
	firsts := map[string]func() (filename, error){}
	for _, method := range grid_resamplings {
		firsts[method] = func() (filename, error) { return rebuild(method) }

		if !single && res > 0 {
			as2021 := chosen
			as2021.Numbertype = "Float32"
			firsts[method+grid_resampled_first] = func() (filename, error) {
				return warp_on_grid_2021(src, ref, g, method, as2021, res)
			}
		}
	}

	if !single {
		for i, method := range []string{"sum", "average"} {
			out, err := rebuild(method)
			if err != nil {
				check.Status, check.Note = infer_failed, fmt.Sprintf("%s: %s", method, err)
				return chosen, check
			}

			pr, err := grid_compare(published, out, g, (i+1)*per, per, edge)
			trash(out)
			if err != nil {
				check.Status, check.Note = infer_failed, err.Error()
				return chosen, check
			}

			check.Agreement[method] = pr.Agree
			notes = append(notes, fmt.Sprintf("%s %.1f%%, total %+.2f%%", method, 100*pr.Agree, 100*pr.Drift))
		}
	}

	profiles := map[string]grid_profile{}
	for name, first := range firsts {
		out, err := first()
		if err != nil {
			continue
		}

		pr, err := grid_compare(published, out, g, 0, per, edge)
		trash(out)
		if err != nil {
			continue
		}

		profiles[name] = pr
		check.Agreement["resample "+name] = pr.Agree
		check.Agreement["exactly "+name] = pr.Exact
	}

	if len(profiles) == 0 {
		check.Status, check.Note = infer_failed, "no resampling could be rebuilt"
		return chosen, check
	}

	method := func(name string) string { return strings.TrimSuffix(name, grid_resampled_first) }

	ranked := make([]string, 0, len(profiles))
	for name := range profiles {
		ranked = append(ranked, name)
	}
	sort.Slice(ranked, func(a, b int) bool {
		pa, pb := profiles[ranked[a]], profiles[ranked[b]]
		if pa.Agree != pb.Agree {
			return pa.Agree > pb.Agree
		}
		if (method(ranked[a]) == cfg.Resample) != (method(ranked[b]) == cfg.Resample) {
			return method(ranked[a]) == cfg.Resample
		}
		return ranked[a] < ranked[b]
	})

	// The category's resampling when it passes, else the closest that does.
	fit := ""
	for _, name := range ranked {
		if method(name) == cfg.Resample && profiles[name].failure() == "" {
			fit = name
			break
		}
	}
	for _, name := range ranked {
		if fit == "" && profiles[name].failure() == "" {
			fit = name
		}
	}

	shown := fit
	if fit == "" {
		shown = ranked[0]
	} else if method(fit) != cfg.Resample {
		chosen.Resample = method(fit)
		notes = append(notes, fmt.Sprintf("resampling %s, not the category's %s", chosen.Resample, cfg.Resample))
	}

	pr := profiles[shown]
	check.Profile = &pr
	notes = append(notes, fmt.Sprintf("%s: %s", shown, pr))

	if fit != "" {
		check.Status = infer_matched
	} else {
		notes = append(notes, "not matched: "+pr.failure())
	}

	check.Note = strings.Join(notes, "; ")

	return chosen, check
}

func raster_band_count(f filename) (int, error) {
	ds, err := gdal.Open(f, gdal.ReadOnly)
	if err != nil {
		return 0, err
	}
	defer ds.Close()

	return ds.RasterCount(), nil
}

func warp_on_grid(in, ref filename, g published_grid, method, numbertype string, nodata int) (filename, error) {
	layer, err := crop_history_layer(ref)
	if err != nil {
		return "", err
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	xmin, ymax := g.gt[0], g.gt[3]
	xmax, ymin := xmin+g.gt[1]*float64(g.x), ymax+g.gt[5]*float64(g.y)

	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

	out := _filename()

	release := capture()
	dest, err := gdal.Warp(out, nil, []gdal.Dataset{src}, []string{
		"-of", "GTiff",
		"-t_srs", g.proj,
		"-te", f(xmin), f(math.Min(ymin, ymax)), f(xmax), f(math.Max(ymin, ymax)),
		"-ts", strconv.Itoa(g.x), strconv.Itoa(g.y),
		"-r", method,
		"-ot", numbertype,
		"-dstnodata", strconv.Itoa(nodata),
		"-cutline", ref,
		"-cl", layer,
		"-wo", "NUM_THREADS=ALL_CPUS",
	})
	if result := release(); err != nil {
		trash(out)
		return "", errors.New(result)
	}
	dest.Close()

	return out, nil
}

// warp_on_grid_2021 is the 2021 crop's two warps: the source resampled to the
// geography's resolution, then cropped onto the grid by nearest neighbour.
func warp_on_grid_2021(in, ref filename, g published_grid, method string, rc raster_config, res int) (filename, error) {
	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	resampled := _filename()
	defer trash(resampled)

	release := capture()
	rs, err := gdal.Warp(resampled, nil, []gdal.Dataset{src}, []string{
		"-of", "GTiff",
		"-t_srs", "EPSG:3857",
		"-tr", strconv.Itoa(res), strconv.Itoa(res),
		"-r", method,
		"-wo", "NUM_THREADS=ALL_CPUS",
	})
	if result := release(); err != nil {
		return "", errors.New(result)
	}
	rs.Close()

	return warp_on_grid(resampled, ref, g, "near", rc.Numbertype, rc.Nodata)
}

// grid_profile is how a rebuild differs from the published bands, as shares
// of the cells with data in either.
type grid_profile struct {
	// Within grid_tolerance of the band's range, and exactly.
	Agree float64 `json:"agree"`
	Exact float64 `json:"exact"`
	// Data in one raster only, away from the outline's edge and at it.
	Interior float64 `json:"interior"`
	Edge     float64 `json:"edge"`
	// The 95th percentile of the differences, as a share of the band's range.
	Deviation float64 `json:"deviation"`
	// Beyond grid_tolerance, where both have data.
	Off float64 `json:"off"`
	// The rebuilt total less the published one, as a share of it.
	Drift   float64 `json:"drift"`
	Classes bool    `json:"classes,omitempty"`
}

// failure says why the rebuild is not the published raster's, or nothing.
func (p grid_profile) failure() string {
	switch {
	case p.Interior > grid_interior_share:
		return fmt.Sprintf("data in one raster only on %.2f%% of the cells inside the outline", 100*p.Interior)
	case p.Edge > grid_edge_share:
		return fmt.Sprintf("data in one raster only on %.1f%% of the cells, at the outline's edge", 100*p.Edge)
	case p.Classes && p.Off > grid_class_share:
		return fmt.Sprintf("another class on %.2f%% of the cells", 100*p.Off)
	case !p.Classes && p.Deviation > grid_deviation:
		return fmt.Sprintf("the 95th percentile difference is %.1f%% of the range", 100*p.Deviation)
	case math.Abs(p.Drift) > grid_drift:
		return fmt.Sprintf("the total differs by %+.2f%%", 100*p.Drift)
	}
	return ""
}

func (p grid_profile) String() string {
	off := fmt.Sprintf("95th percentile %.2f%% of the range", 100*p.Deviation)
	if p.Classes {
		off = fmt.Sprintf("another class on %.2f%%", 100*p.Off)
	}
	return fmt.Sprintf("%.1f%% agree; data in one only on %.2f%% inside, %.2f%% at the edge; %s; total %+.2f%%",
		100*p.Agree, 100*p.Interior, 100*p.Edge, off, 100*p.Drift)
}

// outline_edge marks the cells within raster_edge_reach of where the outline
// cuts the published grid, by the crop's own cut: a grid of ones warped
// through it.
func outline_edge(ref filename, g published_grid) ([]bool, error) {
	drv, err := gdal.GetDriverByName("GTiff")
	if err != nil {
		return nil, err
	}

	ones := _filename()
	defer trash(ones)

	ds := drv.Create(ones, g.x, g.y, 1, gdal.Byte, nil)
	ds.SetProjection(g.proj)
	ds.SetGeoTransform(g.gt)
	ds.RasterBand(1).Fill(1, 0)
	ds.Close()

	cut, err := warp_on_grid(ones, ref, g, "near", "Byte", 0)
	if err != nil {
		return nil, err
	}
	defer trash(cut)

	dc, err := gdal.Open(cut, gdal.ReadOnly)
	if err != nil {
		return nil, err
	}
	defer dc.Close()

	inside := make([]bool, g.x*g.y)
	row := make([]float64, g.x)
	for y := 0; y < g.y; y++ {
		if err := dc.RasterBand(1).IO(gdal.Read, 0, y, g.x, 1, row, g.x, 1, 0, 0); err != nil {
			return nil, err
		}
		for x, v := range row {
			inside[y*g.x+x] = v == 1
		}
	}

	at := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < g.x && y < g.y && inside[y*g.x+x]
	}

	edge := make([]bool, g.x*g.y)
	r := raster_edge_reach
	for y := -1; y < g.y; y++ {
		for x := -1; x < g.x; x++ {
			if at(x, y) == at(x+1, y) && at(x, y) == at(x, y+1) {
				continue
			}
			for v := max(0, y-r); v <= min(g.y-1, y+r+1); v++ {
				for u := max(0, x-r); u <= min(g.x-1, x+r+1); u++ {
					edge[v*g.x+u] = true
				}
			}
		}
	}

	return edge, nil
}

// grid_compare profiles the rebuilt raster's first n bands against the
// published ones from first; edge is outline_edge's.
func grid_compare(published, rebuilt filename, g published_grid, first, n int, edge []bool) (grid_profile, error) {
	var p grid_profile

	dp, err := gdal.Open(published, gdal.ReadOnly)
	if err != nil {
		return p, err
	}
	defer dp.Close()

	dr, err := gdal.Open(rebuilt, gdal.ReadOnly)
	if err != nil {
		return p, err
	}
	defer dr.Close()

	if dr.RasterXSize() != g.x || dr.RasterYSize() != g.y {
		return p, fmt.Errorf("rebuilt %dx%d, published %dx%d", dr.RasterXSize(), dr.RasterYSize(), g.x, g.y)
	}

	n = min(n, dr.RasterCount(), dp.RasterCount()-first)
	if n < 1 {
		return p, errors.New("no band to compare")
	}

	// Differences as a share of the band's range, in bins of 1e-4.
	const bins = 10000
	histogram := make([]int, bins+1)

	agree, exact, counted, both, off, interior, atedge := 0, 0, 0, 0, 0, 0, 0
	var totalp, totalr, scale float64
	classes := true

	for k := 0; k < n; k++ {
		bp, br := dp.RasterBand(first+k+1), dr.RasterBand(k+1)
		np, okp := bp.NoDataValue()
		nr, okr := br.NoDataValue()

		lo, hi := bp.ComputeMinMax(0)
		span := hi - lo
		tolerance := grid_tolerance * span
		distinct := map[float64]bool{}

		const rows = 256
		for y := 0; y < g.y; y += rows {
			h := min(rows, g.y-y)
			vp := make([]float64, g.x*h)
			vr := make([]float64, g.x*h)

			if err := bp.IO(gdal.Read, 0, y, g.x, h, vp, g.x, h, 0, 0); err != nil {
				return p, err
			}
			if err := br.IO(gdal.Read, 0, y, g.x, h, vr, g.x, h, 0, 0); err != nil {
				return p, err
			}

			for i := range vp {
				ep := (okp && vp[i] == np) || math.IsNaN(vp[i])
				er := (okr && vr[i] == nr) || math.IsNaN(vr[i])

				if ep && er {
					continue
				}

				counted++
				if !ep {
					totalp += vp[i]
					scale += math.Abs(vp[i])
					if classes {
						distinct[vp[i]] = true
						classes = vp[i] == math.Trunc(vp[i]) && len(distinct) <= grid_classes
					}
				}
				if !er {
					totalr += vr[i]
				}

				if ep || er {
					if edge[y*g.x+i] {
						atedge++
					} else {
						interior++
					}
					continue
				}

				both++
				d := math.Abs(vp[i] - vr[i])
				if d <= tolerance {
					agree++
					if d == 0 {
						exact++
					}
				} else {
					off++
				}

				b := bins
				if span > 0 && d < span {
					b = int(bins * d / span)
				} else if d == 0 {
					b = 0
				}
				histogram[b]++
			}
		}

	}

	if counted == 0 {
		return p, errors.New("no cell has data")
	}

	share := func(c int) float64 { return float64(c) / float64(counted) }

	p.Agree, p.Exact = share(agree), share(exact)
	p.Interior, p.Edge = share(interior), share(atedge)
	p.Off, p.Classes = share(off), classes
	if scale > 0 {
		p.Drift = (totalr - totalp) / scale
	}

	for b, seen := 0, 0; b <= bins && both > 0; b++ {
		seen += histogram[b]
		if float64(seen) >= 0.95*float64(both) {
			p.Deviation = float64(b) / bins
			break
		}
	}

	return p, nil
}
