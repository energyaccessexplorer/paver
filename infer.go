package main

// `paver infer` reconstructs the parameters a published dataset's paver
// outputs were made with. Admin's paving dialog does not store them, so the
// only evidence is the outputs themselves: candidate parameters are tried in
// the order admin's own defaults would produce them, each candidate is run
// through the routine that made the file, and the first one whose every output
// matches the published one is the answer.
//
// A candidate is never published. The routine runs with no bucket, which makes
// cleanup leave its outputs in -tmpdir for the comparison, and they are deleted
// as soon as they have been read. What each job settled on is appended to the
// manifest; a job whose inputs (routine, the ETags of everything it read, the
// deliverable version) still have a settled entry is skipped, so a run resumes
// and a rerun only writes what it recorded before.
//
// Published files may have been moved by repave: -repoints maps an endpoint
// back to the one the dataset named before, which is what the routine is
// compared against.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	stdcsv "encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/energyaccessexplorer/gdal"
)

// A check's verdict on a candidate. reproduced: every output identical.
// equivalent: every output matches within a stated tolerance, which is what a
// different GDAL build may cost. differs: the best candidate still differs.
// failed: the routine could not run. unchecked: recorded before it ran.
const (
	infer_reproduced = "reproduced"
	infer_equivalent = "equivalent"
	infer_differs    = "differs"
	infer_failed     = "failed"
	infer_unchecked  = "unchecked"
)

const (
	infer_origin_inferred = "inferred"
	infer_origin_admin    = "admin"
)

// A rebuild by another GDAL agrees with the published coordinates to about a
// centimetre, and with a float sample to a millionth of its value.
const (
	infer_decimals = 6
	infer_epsilon  = 1e-6
)

// infer_candidate is one parameter set to try, with what chose it.
type infer_candidate struct {
	params   routine_params
	describe string
}

// One line of the job list: a JSON object from params.sql.
type infer_job struct {
	Dataset    string            `json:"dataset"`
	Geography  string            `json:"geography,omitempty"`
	Root       string            `json:"root,omitempty"`
	Type       string            `json:"type,omitempty"`
	Routine    string            `json:"routine"`
	DatasetURL string            `json:"dataseturl,omitempty"`
	Reference  string            `json:"referenceurl,omitempty"`
	Base       string            `json:"baseurl,omitempty"`
	Resolution int               `json:"resolution,omitempty"`
	Simplify   *float32          `json:"category_simplify,omitempty"`
	Config     raster_config     `json:"config,omitempty"`
	Vectors    json.RawMessage   `json:"vectors_configuration,omitempty"`
	Outputs    map[string]string `json:"outputs"`

	// What the dataset's column holds now: an admin paving is rerun as it is
	// and only its check updated, never its routine or parameters.
	Existing *infer_entry `json:"existing,omitempty"`
}

// infer_check is the column's check: what the comparison settled on.
type infer_check struct {
	Status string `json:"status"`
	// By output function: reproduced, equivalent or differs.
	Outputs map[string]string `json:"outputs,omitempty"`
	// How many parameter sets were run: more than one is the dialog's earlier
	// choices not reproducing the file.
	Candidates int    `json:"candidates"`
	Note       string `json:"note,omitempty"`
	At         string `json:"at"`
}

// infer_entry is one manifest line: the dataset's column as inferred, plus
// what the job read, so a rerun can tell whether its answer still holds.
type infer_entry struct {
	Dataset   string         `json:"dataset"`
	Geography string         `json:"geography,omitempty"`
	Routine   string         `json:"routine"`
	Params    map[string]any `json:"params,omitempty"`
	Origin    string         `json:"origin"`
	Check     infer_check    `json:"check"`
	At        string         `json:"at"`
	Key       string         `json:"key,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type infer_vectors_configuration struct {
	VectorsID string `json:"vectors_id"`
}

var infer_routines = map[string]routine{
	"admin-boundaries": routine_admin_boundaries,
	"clip-proximity":   routine_clip_proximity,
	"crop-raster":      routine_crop_raster,
	"csv-points":       routine_csv_points,
	"csv-raster":       routine_csv_raster,
	"simplify":         routine_simplify,
}

func infer_main(args []string) error {
	fs := flag.NewFlagSet("infer", flag.ExitOnError)

	list := fs.String("list", "", "Job list: JSON lines from params.sql")
	manifest := fs.String("manifest", "", "JSON-lines manifest; appended to, and read to resume")
	repoints := fs.String("repoints", "", "Repoints file: compare against the endpoints the datasets named before repave moved them")
	jobs := fs.Int("j", 2, "Jobs run at once")

	fs.StringVar(&tmpdir, "tmpdir", os.TempDir(), "")

	fs.Parse(args)

	logger = log.New(os.Stderr, "", log.LstdFlags)

	// Comparing a published raster reads it whole, and a job runs beside
	// another: GDAL's block cache is bounded so both fit.
	if os.Getenv("GDAL_CACHEMAX") == "" {
		os.Setenv("GDAL_CACHEMAX", "192")
	}
	if os.Getenv("GDAL_NUM_THREADS") == "" {
		os.Setenv("GDAL_NUM_THREADS", "ALL_CPUS")
	}

	if *list == "" || *manifest == "" {
		return errors.New("infer needs -list and -manifest")
	}

	original := func(dataset, endpoint string) string { return endpoint }
	if *repoints != "" {
		resolve, err := infer_originals(*repoints)
		if err != nil {
			return err
		}
		original = resolve
	}

	todo, err := infer_read_jobs(*list)
	if err != nil {
		return err
	}

	for i := range todo {
		for fn, endpoint := range todo[i].Outputs {
			todo[i].Outputs[fn] = original(todo[i].Dataset, endpoint)
		}
	}

	previous, err := infer_manifest_read(*manifest)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(*manifest, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	var mu sync.Mutex
	record := func(e infer_entry) {
		mu.Lock()
		defer mu.Unlock()

		j, _ := json.Marshal(e)
		out.Write(append(j, '\n'))
		previous[e.Dataset] = e

		logger.Printf("%-11s %-16s %s %s", e.Check.Status, e.Routine, e.Dataset, e.Check.Note)
	}

	queue := make(chan infer_job)
	var wg sync.WaitGroup

	for i := 0; i < max(1, *jobs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				key, err := infer_key(job)
				if err != nil {
					record(infer_entry{Dataset: job.Dataset, Routine: job.Routine, Origin: infer_origin(job), Error: err.Error(),
						Check: infer_check{Status: infer_failed, Note: err.Error(), At: infer_now()}})
					continue
				}

				mu.Lock()
				e, ok := previous[job.Dataset]
				mu.Unlock()

				if ok && infer_settled(e, key) {
					continue
				}

				record(infer_one(job, key))
			}
		}()
	}

	for _, job := range todo {
		queue <- job
	}
	close(queue)
	wg.Wait()

	infer_summary(previous)

	return nil
}

func infer_now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// infer_origin says who chose the parameters: an admin paving is rerun as it
// is recorded, everything else is inferred from the outputs.
func infer_origin(j infer_job) string {
	if j.Existing != nil && j.Existing.Origin == infer_origin_admin && len(j.Existing.Params) > 0 {
		return infer_origin_admin
	}

	return infer_origin_inferred
}

// infer_settled says a previous run's entry needs no more work, given the
// job's key now. A run that could not be made is retried.
func infer_settled(e infer_entry, key string) bool {
	if e.Key != key {
		return false
	}

	switch e.Check.Status {
	case infer_reproduced, infer_equivalent, infer_differs:
		return true
	default:
		return false
	}
}

// infer_key identifies a job's inputs: the routine, the encoding version, and
// what it reads -- the sources, and the published files it is compared with.
func infer_key(j infer_job) (string, error) {
	parts := []string{j.Routine, deliverable_version}

	reads := []string{j.DatasetURL, j.Reference, j.Base}

	funcs := make([]string, 0, len(j.Outputs))
	for fn := range j.Outputs {
		funcs = append(funcs, fn)
	}
	sort.Strings(funcs)

	for _, fn := range funcs {
		reads = append(reads, j.Outputs[fn])
	}

	for _, url := range reads {
		if url == "" {
			continue
		}

		v, err := object_version(url)
		if err != nil {
			return "", err
		}
		parts = append(parts, v)
	}

	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))

	return hex.EncodeToString(h[:]), nil
}

// infer_originals maps an endpoint repave moved back to the one a dataset
// named before, by dataset, following the chain a reference moved twice makes.
func infer_originals(f filename) (func(dataset, endpoint string) string, error) {
	moves := map[string]string{}

	file, err := os.Open(f)
	if errors.Is(err, os.ErrNotExist) {
		return func(_, endpoint string) string { return endpoint }, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var last []repoint

	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	for sc.Scan() {
		var l repoint
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		last = append(last, l)
	}

	if err := sc.Err(); err != nil {
		return nil, err
	}

	// A pair's last applied or restored line is its state, as in repoint.go.
	state := map[repoint_pair]repoint{}
	for _, l := range last {
		if l.Status == repoint_applied || l.Status == repoint_restored {
			state[repoint_pair{l.Dataset, l.From}] = l
		}
	}

	for _, l := range state {
		if l.Status == repoint_applied {
			moves[l.Dataset+"\x00"+l.To] = l.From
		}
	}

	return func(dataset, endpoint string) string {
		for i := 0; i < 8; i++ {
			from, ok := moves[dataset+"\x00"+endpoint]
			if !ok {
				return endpoint
			}
			endpoint = from
		}
		return endpoint
	}, nil
}

func infer_one(j infer_job, key string) infer_entry {
	e := infer_entry{Dataset: j.Dataset, Geography: j.Geography, Routine: j.Routine, Origin: infer_origin(j), Key: key}
	e.At = infer_now()
	e.Check = infer_check{Status: infer_unchecked, At: e.At}

	src, ref, base, published, err := infer_download(j)
	if err != nil {
		e.Check.Status, e.Check.Note = infer_failed, err.Error()
		return e
	}

	defer infer_trash(map[string]filename{"dataseturl": src, "referenceurl": ref, "baseurl": base})
	defer infer_trash(published)

	e.Params, e.Check = infer_settle(j, src, ref, base, published)

	return e
}

// infer_settle runs the job's candidates against the published outputs and
// returns the parameters that fit best, with what the comparison said.
func infer_settle(j infer_job, src, ref, base filename, published map[string]filename) (map[string]any, infer_check) {
	candidates, err := infer_candidates(j, src, ref, base, published)
	if err != nil {
		return nil, infer_check{Status: infer_failed, Note: err.Error(), At: infer_now()}
	}

	best := infer_check{Note: "no candidate ran"}
	failures := []string{}
	params := map[string]any{}

	for i, c := range candidates {
		tried := i + 1

		rebuilt, err := infer_run(j.Routine, c.params)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", c.describe, err.Error()))
			continue
		}

		status, outputs, note := infer_verdict(published, rebuilt)
		infer_trash(rebuilt)

		if note != "" {
			note = c.describe + ": " + note
		} else {
			note = c.describe
		}

		if status == infer_reproduced {
			return infer_params_map(j.Routine, c.params),
				infer_check{Status: status, Outputs: outputs, Candidates: tried, Note: note, At: infer_now()}
		}

		if infer_rank(status) > infer_rank(best.Status) {
			best = infer_check{Status: status, Outputs: outputs, Note: note, At: infer_now()}
			params = infer_params_map(j.Routine, c.params)
		}

		best.Candidates = tried
	}

	if best.Status == "" {
		return nil, infer_check{Status: infer_failed, Candidates: len(candidates), Note: strings.Join(failures, "; "), At: infer_now()}
	}

	if len(failures) > 0 {
		best.Note = strings.TrimSuffix(best.Note+"; failed: "+strings.Join(failures, "; "), "; ")
	}

	return params, best
}

// infer_rank orders verdicts so that a later candidate only replaces a
// recorded one when it is closer to the published output.
func infer_rank(status string) int {
	switch status {
	case infer_reproduced:
		return 3
	case infer_equivalent:
		return 2
	case infer_differs:
		return 1
	default:
		return 0
	}
}

// infer_download fetches what the job reads: the sources the routine needs,
// and every published output to compare against.
func infer_download(j infer_job) (src, ref, base filename, published map[string]filename, err error) {
	published = map[string]filename{}

	if j.DatasetURL != "" {
		if src, err = infer_fetch(j.DatasetURL); err != nil {
			return "", "", "", nil, fmt.Errorf("source: %w", err)
		}
	}

	if j.Reference != "" {
		if ref, err = infer_fetch(j.Reference); err != nil {
			infer_trash(map[string]filename{"dataseturl": src})
			return "", "", "", nil, fmt.Errorf("reference: %w", err)
		}
	}

	if j.Base != "" {
		if base, err = infer_fetch(j.Base); err != nil {
			infer_trash(map[string]filename{"dataseturl": src, "referenceurl": ref})
			return "", "", "", nil, fmt.Errorf("base: %w", err)
		}
	}

	for fn, endpoint := range j.Outputs {
		f, err := infer_fetch(endpoint)
		if err != nil {
			infer_trash(map[string]filename{"dataseturl": src, "referenceurl": ref, "baseurl": base})
			infer_trash(published)
			return "", "", "", nil, fmt.Errorf("published %s: %w", fn, err)
		}

		published[fn] = f
	}

	return src, ref, base, published, nil
}

// infer_fetch downloads url as paver itself does, to a name with no extension:
// a routine derives what it needs from the content, and csv_points takes its
// layer's SQL name from the stem.
func infer_fetch(url string) (filename, error) {
	return download(url)
}

func infer_trash(files map[string]filename) {
	for _, f := range files {
		if f != "" {
			trash(f)
		}
	}
}

// infer_run runs a routine with no bucket: cleanup leaves the outputs where
// they are for the comparison instead of publishing them. Each input is
// hard-linked first, because the routines rename what they are given.
func infer_run(routine_name string, p routine_params) (map[string]filename, error) {
	fn, ok := infer_routines[routine_name]
	if !ok {
		return nil, errors.New("no routine " + routine_name)
	}

	p.S3 = s3config{}
	p.Dataset = infer_link(p.Dataset)
	p.Reference = infer_link(p.Reference)
	p.Base = infer_link(p.Base)

	jsonstr, err := fn(noop_reporter, p)
	if err != nil {
		return nil, err
	}

	return infer_outputs(jsonstr), nil
}

func infer_link(f filename) filename {
	if f == "" {
		return ""
	}

	out := _filename() + path.Ext(f)
	if err := os.Link(f, out); err != nil {
		return f
	}

	return out
}

// infer_outputs finds the files a routine's reply names: paver writes them
// into tmpdir under the uuid in the URL it returns.
func infer_outputs(jsonstr string) map[string]filename {
	files := map[string]filename{}

	var reply map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonstr), &reply); err != nil {
		return files
	}

	for fn, raw := range reply {
		var url string
		if json.Unmarshal(raw, &url) != nil {
			continue
		}

		id := _uuid(url)
		if id == "" {
			continue
		}

		m, _ := filepath.Glob(tmpdir + "/" + id + "*")
		if len(m) > 0 {
			files[fn] = m[0]
		}
	}

	return files
}

// infer_verdict compares every output the routine produces with the one the
// dataset published.
func infer_verdict(published, rebuilt map[string]filename) (string, map[string]string, string) {
	statuses := map[string]string{}
	notes := []string{}
	overall := ""

	funcs := make([]string, 0, len(published))
	for fn := range published {
		funcs = append(funcs, fn)
	}
	sort.Strings(funcs)

	for _, fn := range funcs {
		re, ok := rebuilt[fn]
		if !ok {
			notes = append(notes, fn+" not produced")
			continue
		}

		status, note := infer_compare(fn, published[fn], re)
		statuses[fn] = status
		if note != "" {
			notes = append(notes, note)
		}

		if overall == "" || infer_rank(status) < infer_rank(overall) {
			overall = status
		}
	}

	if overall == "" {
		return infer_differs, statuses, "no published output this routine produces"
	}

	return overall, statuses, strings.Join(notes, "; ")
}

func infer_compare(fn string, published, rebuilt filename) (string, string) {
	switch fn {
	case "raster":
		return infer_compare_rasters(published, rebuilt)
	case "vectors":
		return vectors_compare(published, rebuilt)
	case "csv":
		return csv_compare(published, rebuilt)
	}

	if err := infer_compare_bytes(published, rebuilt); err != nil {
		return infer_differs, fn + ": " + err.Error()
	}

	return infer_reproduced, ""
}

func infer_compare_bytes(a, b filename) error {
	sa, err := file_sha256(a)
	if err != nil {
		return err
	}

	sb, err := file_sha256(b)
	if err != nil {
		return err
	}

	if sa != sb {
		return errors.New("bytes differ")
	}

	return nil
}

func infer_compare_rasters(published, rebuilt filename) (string, string) {
	err := rasters_identical(published, rebuilt)
	if err == nil {
		return infer_reproduced, ""
	}

	if e := rasters_equivalent(published, rebuilt); e == nil {
		return infer_equivalent, "raster: values within 1e-6, coordinate within 1e-9"
	}

	if note := rasters_grid_note(published, rebuilt); note != "" {
		return infer_differs, note
	}

	return infer_differs, "raster: " + err.Error()
}

// rasters_equivalent accepts what a different GDAL build may change: the last
// bits of a geotransform, how it spells a projection, and float noise in a
// sample. Every sample within infer_epsilon of the published one counts.
func rasters_equivalent(a, b filename) error {
	da, err := gdal.Open(a, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer da.Close()

	db, err := gdal.Open(b, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer db.Close()

	if da.RasterXSize() != db.RasterXSize() || da.RasterYSize() != db.RasterYSize() {
		return fmt.Errorf("size %dx%d vs %dx%d", da.RasterXSize(), da.RasterYSize(), db.RasterXSize(), db.RasterYSize())
	}

	if da.RasterCount() != db.RasterCount() {
		return fmt.Errorf("band count %d vs %d", da.RasterCount(), db.RasterCount())
	}

	if !geotransforms_equal(da.GeoTransform(), db.GeoTransform()) {
		return errors.New("geotransform differs")
	}

	if !projections_equal(da.Projection(), db.Projection()) {
		return errors.New("projection differs")
	}

	width, height := da.RasterXSize(), da.RasterYSize()

	for i := 1; i <= da.RasterCount(); i++ {
		ba, bb := da.RasterBand(i), db.RasterBand(i)

		if ba.RasterDataType() != bb.RasterDataType() {
			return fmt.Errorf("band %d: type %v vs %v", i, ba.RasterDataType(), bb.RasterDataType())
		}

		na, oka := ba.NoDataValue()
		nb, okb := bb.NoDataValue()
		if oka != okb || (oka && math.Float64bits(na) != math.Float64bits(nb)) {
			return fmt.Errorf("band %d: nodata %v/%v vs %v/%v", i, na, oka, nb, okb)
		}

		if err := band_samples_equivalent(ba, bb, width, height); err != nil {
			return fmt.Errorf("band %d: %w", i, err)
		}
	}

	return nil
}

func band_samples_equivalent(a, b gdal.RasterBand, width, height int) error {
	const rows = 256

	for y := 0; y < height; y += rows {
		n := min(rows, height-y)

		va := make([]float64, width*n)
		vb := make([]float64, width*n)

		if err := a.IO(gdal.Read, 0, y, width, n, va, width, n, 0, 0); err != nil {
			return err
		}
		if err := b.IO(gdal.Read, 0, y, width, n, vb, width, n, 0, 0); err != nil {
			return err
		}

		for k := range va {
			if !floats_close(va[k], vb[k]) {
				return fmt.Errorf("samples differ in rows %d..%d", y, y+n-1)
			}
		}
	}

	return nil
}

func floats_close(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}

	return math.Abs(a-b) <= infer_epsilon*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// rasters_grid_note says the rasters agree in everything but their grid: a
// published crop may predate the crop that snaps cells to the outline raster's
// grid, and no parameters put it back on that grid. Anything else is left to
// the comparison's own error.
func rasters_grid_note(published, rebuilt filename) string {
	p, err := gdal.Open(published, gdal.ReadOnly)
	if err != nil {
		return ""
	}
	defer p.Close()

	r, err := gdal.Open(rebuilt, gdal.ReadOnly)
	if err != nil {
		return ""
	}
	defer r.Close()

	if p.RasterCount() != r.RasterCount() || !projections_equal(p.Projection(), r.Projection()) {
		return ""
	}

	pgt, rgt := p.GeoTransform(), r.GeoTransform()
	if pgt == rgt {
		return ""
	}

	for i := 1; i <= p.RasterCount(); i++ {
		if p.RasterBand(i).RasterDataType() != r.RasterBand(i).RasterDataType() {
			return ""
		}
	}

	return fmt.Sprintf("raster: the published grid (%gx%g m, origin %g %g) is not the outline grid the crop uses now (%gx%g m, origin %g %g)",
		math.Abs(pgt[1]), math.Abs(pgt[5]), pgt[0], pgt[3], math.Abs(rgt[1]), math.Abs(rgt[5]), rgt[0], rgt[3])
}

// feature_stream reads a GeoJSON file's features one at a time: a national
// points file does not fit in memory.
type feature_stream struct {
	file *os.File
	dec  *json.Decoder
}

func open_features(f filename) (*feature_stream, error) {
	file, err := os.Open(f)
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bufio.NewReaderSize(file, 1<<20))

	depth := 0

	for {
		t, err := dec.Token()
		if err != nil {
			file.Close()
			return nil, err
		}

		if d, ok := t.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth <= 0 {
					file.Close()
					return nil, errors.New("no features")
				}
			}
			continue
		}

		if key, ok := t.(string); ok && key == "features" && depth == 1 {
			t, err := dec.Token()
			if err != nil {
				file.Close()
				return nil, err
			}
			if d, ok := t.(json.Delim); !ok || d != '[' {
				file.Close()
				return nil, errors.New("features is not a list")
			}
			return &feature_stream{file: file, dec: dec}, nil
		}
	}
}

func (s *feature_stream) next() (json.RawMessage, error) {
	if !s.dec.More() {
		return nil, io.EOF
	}

	var raw json.RawMessage
	if err := s.dec.Decode(&raw); err != nil {
		return nil, err
	}

	return raw, nil
}

func (s *feature_stream) close() { s.file.Close() }

// feature_canonical is what a feature is, independent of key order: its
// properties at full precision, and its geometry (plus id) at decimals.
func feature_canonical(raw json.RawMessage, decimals int) (string, error) {
	var f struct {
		ID         json.RawMessage `json:"id"`
		Properties json.RawMessage `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	}

	if err := json.Unmarshal(raw, &f); err != nil {
		return "", err
	}

	var b strings.Builder

	props, err := canonical_json(f.Properties, -1)
	if err != nil {
		return "", err
	}

	geom, err := canonical_json(f.Geometry, decimals)
	if err != nil {
		return "", err
	}

	id, err := canonical_json(f.ID, -1)
	if err != nil {
		return "", err
	}

	b.WriteString(id)
	b.WriteString("|")
	b.WriteString(props)
	b.WriteString("|")
	b.WriteString(geom)

	return b.String(), nil
}

// canonical_json writes v with object keys sorted and numbers in their
// shortest exact form, rounded to decimals when it is not negative.
func canonical_json(raw json.RawMessage, decimals int) (string, error) {
	if len(raw) == 0 {
		return "null", nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}

	var b strings.Builder
	write_canonical(&b, v, decimals)

	return b.String(), nil
}

func write_canonical(b *strings.Builder, v any, decimals int) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(k))
			b.WriteByte(':')
			write_canonical(b, x[k], decimals)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			write_canonical(b, e, decimals)
		}
		b.WriteByte(']')
	case json.Number:
		b.WriteString(canonical_number(x, decimals))
	case string:
		b.WriteString(strconv.Quote(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case nil:
		b.WriteString("null")
	default:
		b.WriteString(fmt.Sprintf("%v", x))
	}
}

func canonical_number(n json.Number, decimals int) string {
	f, err := n.Float64()
	if err != nil {
		return n.String()
	}

	if decimals >= 0 {
		return strconv.FormatFloat(f, 'f', decimals, 64)
	}

	return strconv.FormatFloat(f, 'g', -1, 64)
}

// features_compare reads both files in step, comparing features in order.
func features_compare(a, b filename, decimals int) error {
	fa, err := open_features(a)
	if err != nil {
		return err
	}
	defer fa.close()

	fb, err := open_features(b)
	if err != nil {
		return err
	}
	defer fb.close()

	for n := 0; ; n++ {
		ra, ea := fa.next()
		rb, eb := fb.next()

		switch {
		case ea == io.EOF && eb == io.EOF:
			return nil
		case ea == io.EOF:
			return fmt.Errorf("the published file has %d features more", n)
		case eb == io.EOF:
			return fmt.Errorf("the published file has %d features fewer", n)
		case ea != nil:
			return ea
		case eb != nil:
			return eb
		}

		ca, err := feature_canonical(ra, decimals)
		if err != nil {
			return err
		}

		cb, err := feature_canonical(rb, decimals)
		if err != nil {
			return err
		}

		if ca != cb {
			return fmt.Errorf("feature %d: %s", n, feature_difference(ra, rb, decimals))
		}
	}
}

// feature_difference names the first thing two features disagree on: a
// property, its value, the geometry, or the coordinates inside it.
func feature_difference(a, b json.RawMessage, decimals int) string {
	type feature struct {
		ID         json.RawMessage            `json:"id"`
		Properties map[string]json.RawMessage `json:"properties"`
		Geometry   json.RawMessage            `json:"geometry"`
	}

	var fa, fb feature
	if err := json.Unmarshal(a, &fa); err != nil {
		return err.Error()
	}
	if err := json.Unmarshal(b, &fb); err != nil {
		return err.Error()
	}

	missing, extra := []string{}, []string{}
	for k := range fa.Properties {
		if _, ok := fb.Properties[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range fb.Properties {
		if _, ok := fa.Properties[k]; !ok {
			extra = append(extra, k)
		}
	}

	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Sprintf("properties %s vs %s", summarise_names(missing), summarise_names(extra))
	}

	keys := make([]string, 0, len(fa.Properties))
	for k := range fa.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		ca, err := canonical_json(fa.Properties[k], -1)
		if err != nil {
			return err.Error()
		}
		cb, err := canonical_json(fb.Properties[k], -1)
		if err != nil {
			return err.Error()
		}
		if ca != cb {
			return fmt.Sprintf("property %s is %s, not %s", k, summarise_value(cb), summarise_value(ca))
		}
	}

	ga, err := canonical_json(fa.Geometry, -1)
	if err != nil {
		return err.Error()
	}
	gb, err := canonical_json(fb.Geometry, -1)
	if err != nil {
		return err.Error()
	}

	if ga != gb {
		if decimals >= 0 {
			ra, _ := canonical_json(fa.Geometry, decimals)
			rb, _ := canonical_json(fb.Geometry, decimals)
			if ra == rb {
				return "coordinates within 1e-6 degrees"
			}
		}
		return "geometry differs"
	}

	return "id differs"
}

func summarise_names(names []string) string {
	if len(names) == 0 {
		return "none"
	}

	if len(names) > 4 {
		return fmt.Sprintf("[%s and %d more]", strings.Join(names[:4], " "), len(names)-4)
	}

	return "[" + strings.Join(names, " ") + "]"
}

func summarise_value(v string) string {
	if len(v) > 60 {
		return v[:57] + "..."
	}

	return v
}

// features_multiset says the two files hold the same features in another
// order. Only reached when the ordered comparison failed, so the memory it
// needs is paid for once.
func features_multiset(a, b filename, decimals int) (bool, error) {
	ha, err := features_hashes(a, decimals)
	if err != nil {
		return false, err
	}

	hb, err := features_hashes(b, decimals)
	if err != nil {
		return false, err
	}

	if len(ha) != len(hb) {
		return false, nil
	}

	sort.Strings(ha)
	sort.Strings(hb)

	for i := range ha {
		if ha[i] != hb[i] {
			return false, nil
		}
	}

	return true, nil
}

func features_hashes(f filename, decimals int) ([]string, error) {
	stream, err := open_features(f)
	if err != nil {
		return nil, err
	}
	defer stream.close()

	hashes := []string{}

	for {
		raw, err := stream.next()
		if err == io.EOF {
			return hashes, nil
		}
		if err != nil {
			return nil, err
		}

		c, err := feature_canonical(raw, decimals)
		if err != nil {
			return nil, err
		}

		h := sha256.Sum256([]byte(c))
		hashes = append(hashes, string(h[:16]))
	}
}

func vectors_compare(published, rebuilt filename) (string, string) {
	err := features_compare(published, rebuilt, -1)
	if err == nil {
		return infer_reproduced, ""
	}

	mismatch := err

	if err := features_compare(published, rebuilt, infer_decimals); err == nil {
		return infer_equivalent, "vectors: coordinates within 1e-6 degrees (about 10 cm)"
	}

	if same, err := features_multiset(published, rebuilt, infer_decimals); err == nil && same {
		return infer_equivalent, "vectors: the same features in a different order"
	}

	return infer_differs, "vectors: " + mismatch.Error()
}

func csv_compare(published, rebuilt filename) (string, string) {
	if err := csv_rows_compare(published, rebuilt, false); err == nil {
		return infer_reproduced, ""
	}

	if err := csv_rows_compare(published, rebuilt, true); err == nil {
		return infer_equivalent, "csv: the same rows in a different order"
	}

	return infer_differs, "csv: rows differ"
}

func csv_rows_compare(a, b filename, sorted bool) error {
	ra, err := csv_read_rows(a)
	if err != nil {
		return err
	}

	rb, err := csv_read_rows(b)
	if err != nil {
		return err
	}

	if !sorted {
		if len(ra) != len(rb) {
			return fmt.Errorf("%d rows vs %d", len(ra), len(rb))
		}

		for i := range ra {
			if strings.Join(ra[i], "\x00") != strings.Join(rb[i], "\x00") {
				return fmt.Errorf("row %d differs", i)
			}
		}

		return nil
	}

	if len(ra) != len(rb) {
		return fmt.Errorf("%d rows vs %d", len(ra), len(rb))
	}

	ka := make([]string, len(ra))
	kb := make([]string, len(rb))

	for i := range ra {
		ka[i] = strings.Join(ra[i], "\x00")
	}
	for i := range rb {
		kb[i] = strings.Join(rb[i], "\x00")
	}

	sort.Strings(ka)
	sort.Strings(kb)

	for i := range ka {
		if ka[i] != kb[i] {
			return errors.New("rows differ")
		}
	}

	return nil
}

func csv_read_rows(f filename) ([][]string, error) {
	file, err := os.Open(f)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	r := stdcsv.NewReader(bufio.NewReaderSize(file, 1<<20))
	r.FieldsPerRecord = -1

	return r.ReadAll()
}

// infer_candidates lists the parameter sets to try, in the order admin's own
// defaults would produce them.
func infer_candidates(j infer_job, src, ref, base filename, published map[string]filename) ([]infer_candidate, error) {
	common := routine_params{Dataset: src, Reference: ref, Base: base, Resolution: j.Resolution, Config: j.Config}

	if infer_origin(j) == infer_origin_admin {
		p, err := infer_params_apply(common, j.Existing.Params)
		if err != nil {
			return nil, err
		}

		return []infer_candidate{{params: p, describe: "as admin paved it"}}, nil
	}

	if src == "" {
		return nil, errors.New("no source file for " + j.Routine)
	}

	switch j.Routine {
	case "crop-raster", "clip-proximity", "csv-points", "csv-raster":
		if ref == "" {
			return nil, fmt.Errorf("%s clips to the geography's first division, which has no published vectors", j.Routine)
		}
	}

	switch j.Routine {
	case "crop-raster":
		if ref == "" || base == "" || j.Config.Resample == "" {
			return nil, errors.New("crop-raster needs the outline vectors, the outline raster and the category's raster->paver config")
		}

		return []infer_candidate{{params: common, describe: "the category's config and the geography's resolution"}}, nil

	case "clip-proximity":
		return infer_clip_proximity_candidates(j, common, published)

	case "csv-points":
		return infer_csv_points_candidates(j, common, published)

	case "csv-raster":
		return infer_csv_raster_candidates(j, common, published)

	case "admin-boundaries":
		return infer_attr_candidates(j, common, published, false)

	case "simplify":
		return infer_attr_candidates(j, common, published, true)
	}

	return nil, errors.New("no candidates for " + j.Routine)
}

// infer_params_map renders a candidate as the column's params: paver's own
// JSON names, without the endpoints, which come from the dataset itself.
func infer_params_map(routine_name string, p routine_params) map[string]any {
	m := map[string]any{"resolution": p.Resolution}

	switch routine_name {
	case "crop-raster":
		m["config"] = p.Config
	case "clip-proximity":
		m["fields"] = p.Fields
		m["dissolve"] = p.Dissolve
		m["simplify"] = p.Simplify
	case "csv-points":
		m["fields"] = p.Fields
		m["lnglat"] = p.LngLat
	case "csv-raster":
		m["lnglat"] = p.LngLat
		m["attr"] = p.Attr
	case "admin-boundaries":
		m["attr"] = p.Attr
	case "simplify":
		m["attr"] = p.Attr
		m["simplify"] = p.Simplify
	}

	return m
}

// infer_params_apply puts a recorded params object onto the job's inputs.
func infer_params_apply(p routine_params, m map[string]any) (routine_params, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return p, err
	}

	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}

	return p, nil
}

func infer_clip_proximity_candidates(j infer_job, p routine_params, published map[string]filename) ([]infer_candidate, error) {
	fields, err := geojson_properties(published["vectors"])
	if err != nil {
		return nil, err
	}

	p.Fields = fields

	simplifies := []float32{}
	if j.Simplify != nil {
		simplifies = append(simplifies, *j.Simplify)
	}
	if !contains_float32(simplifies, 0) {
		simplifies = append(simplifies, 0)
	}

	dissolves := []bool{false}
	if j.Type != "points" {
		dissolves = append(dissolves, true)
	}

	candidates := []infer_candidate{}
	for _, s := range simplifies {
		for _, d := range dissolves {
			c := p
			c.Simplify, c.Dissolve = s, d
			candidates = append(candidates, infer_candidate{params: c, describe: fmt.Sprintf("simplify %g, dissolve %t", s, d)})
		}
	}

	// A file paved before the dialog took the category's simplification may
	// carry one as coarse as the geography's grid.
	if j.Resolution > 0 {
		c := p
		c.Simplify, c.Dissolve = float32(j.Resolution)/111320, false
		if !contains_float32(simplifies, c.Simplify) {
			candidates = append(candidates, infer_candidate{params: c,
				describe: fmt.Sprintf("simplify %g (as coarse as the grid), dissolve false", c.Simplify)})
		}
	}

	return candidates, nil
}

func contains_string(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func contains_float32(s []float32, v float32) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// infer_attr_candidates reads the attribute out of vectors_configuration, and
// falls back to the published properties that identify one feature each.
func infer_attr_candidates(j infer_job, p routine_params, published map[string]filename, simplify bool) ([]infer_candidate, error) {
	attrs := []string{}

	var vc infer_vectors_configuration
	if len(j.Vectors) > 0 {
		if err := json.Unmarshal(j.Vectors, &vc); err != nil {
			return nil, err
		}
	}

	if vc.VectorsID != "" {
		attrs = append(attrs, vc.VectorsID)
	}

	unique, err := geojson_unique_numbers(published["vectors"])
	if err != nil {
		return nil, err
	}

	for _, f := range unique {
		if f != vc.VectorsID {
			attrs = append(attrs, f)
		}
		if len(attrs) >= 4 {
			break
		}
	}

	// The property that produced the ids raster may not be unique in the file
	// paved since, may not be numeric either: it is still what admin could
	// have picked, and running it is what decides.
	if len(attrs) < 4 {
		all, err := geojson_properties(published["vectors"])
		if err != nil {
			return nil, err
		}

		for _, f := range all {
			if len(attrs) >= 4 {
				break
			}
			if !contains_string(attrs, f) {
				attrs = append(attrs, f)
			}
		}
	}

	if len(attrs) == 0 {
		return nil, errors.New("no attribute the published vectors could have been found by")
	}

	simplifies := []float32{0}
	if simplify {
		if j.Simplify != nil && *j.Simplify != 0 {
			simplifies = []float32{*j.Simplify, 0}
		}
	}

	candidates := []infer_candidate{}
	for _, a := range attrs {
		for _, s := range simplifies {
			c := p
			c.Attr, c.Simplify = a, s
			describe := "attr " + a
			if simplify {
				describe = fmt.Sprintf("attr %s, simplify %g", a, s)
			}
			candidates = append(candidates, infer_candidate{params: c, describe: describe})
		}
	}

	return candidates, nil
}

// geojson_properties is the published vectors' property names, sorted: what
// clip-proximity strips the source down to.
func geojson_properties(f filename) ([]string, error) {
	if f == "" {
		return nil, errors.New("no published vectors")
	}

	stream, err := open_features(f)
	if err != nil {
		return nil, err
	}
	defer stream.close()

	seen := map[string]bool{}
	names := []string{}

	for {
		raw, err := stream.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		var feat struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &feat); err != nil {
			return nil, err
		}

		for k := range feat.Properties {
			if !seen[k] {
				seen[k] = true
				names = append(names, k)
			}
		}
	}

	sort.Strings(names)

	return names, nil
}

// geojson_unique_numbers is the published vectors' numeric properties that
// hold a different value in every feature: what admin-boundaries can rasterise
// into ids. Only the first features are read: a candidate only has to be
// plausible, running it is what decides.
func geojson_unique_numbers(f filename) ([]string, error) {
	if f == "" {
		return nil, nil
	}

	stream, err := open_features(f)
	if err != nil {
		return nil, err
	}
	defer stream.close()

	const read = 500

	seen := map[string]map[string]bool{}
	numeric := map[string]bool{}
	order := []string{}
	features := 0

	for features < read {
		raw, err := stream.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		var feat struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &feat); err != nil {
			return nil, err
		}

		features++

		for k, v := range feat.Properties {
			if _, ok := seen[k]; !ok {
				seen[k] = map[string]bool{}
				order = append(order, k)
				numeric[k] = true
			}

			var number float64
			if json.Unmarshal(v, &number) != nil {
				numeric[k] = false
			}

			seen[k][string(v)] = true
		}
	}

	unique := []string{}
	for _, k := range order {
		if numeric[k] && len(seen[k]) == features {
			unique = append(unique, k)
		}
	}

	return unique, nil
}

// infer_csv_points_candidates finds the columns whose values are the published
// points' coordinates. The points are the CSV rows that fell inside the
// geography, so a column pair is right when every sampled point is one of its
// rows.
func infer_csv_points_candidates(j infer_job, p routine_params, published map[string]filename) ([]infer_candidate, error) {
	points, err := geojson_points(published["vectors"])
	if err != nil {
		return nil, err
	}

	if len(points) == 0 {
		return nil, errors.New("no published points to match the csv against")
	}

	fields, err := geojson_properties(published["vectors"])
	if err != nil {
		return nil, err
	}

	p.Fields = fields

	profile, err := csv_profile_read(p.Dataset)
	if err != nil {
		return nil, err
	}

	pairs := profile.lnglat_pairs()
	if len(pairs) == 0 {
		return nil, errors.New("no numeric columns in the csv that could be longitude and latitude")
	}

	scores, err := csv_pair_points(p.Dataset, pairs, points, profile.rows)
	if err != nil {
		return nil, err
	}

	best := best_pairs(pairs, scores, len(points), 4)
	if len(best) == 0 {
		return nil, errors.New("no pair of csv columns reproduces the published points")
	}

	candidates := []infer_candidate{}
	for _, i := range best {
		c := p
		c.LngLat = [2]string{profile.header[pairs[i][0]], profile.header[pairs[i][1]]}
		candidates = append(candidates, infer_candidate{params: c,
			describe: fmt.Sprintf("lnglat %s,%s", c.LngLat[0], c.LngLat[1])})
	}

	return candidates, nil
}

// infer_csv_raster_candidates finds the columns whose values are a longitude
// and latitude landing inside the published raster, and the column whose value
// the raster's cells hold.
func infer_csv_raster_candidates(j infer_job, p routine_params, published map[string]filename) ([]infer_candidate, error) {
	raster := published["raster"]
	if raster == "" {
		return nil, errors.New("no published raster to match the csv against")
	}

	profile, err := csv_profile_read(p.Dataset)
	if err != nil {
		return nil, err
	}

	box, err := raster_bounds_4326(raster)
	if err != nil {
		return nil, err
	}

	pairs := profile.lnglat_pairs()
	if len(pairs) == 0 {
		return nil, errors.New("no numeric columns in the csv that could be longitude and latitude")
	}

	scores, err := csv_pair_bounds(p.Dataset, pairs, box, profile.rows)
	if err != nil {
		return nil, err
	}

	best := best_pairs(pairs, scores, 1, 3)
	if len(best) == 0 {
		return nil, errors.New("no pair of csv columns lands inside the published raster")
	}

	values, err := raster_values(raster)
	if err != nil {
		return nil, err
	}

	attrs := profile.attr_columns(values)
	if len(attrs) == 0 {
		return nil, errors.New("no csv column holds a value the published raster has")
	}

	candidates := []infer_candidate{}
	for _, i := range best {
		for _, a := range attrs {
			if a == pairs[i][0] || a == pairs[i][1] {
				continue
			}
			c := p
			c.LngLat = [2]string{profile.header[pairs[i][0]], profile.header[pairs[i][1]]}
			c.Attr = profile.header[a]
			candidates = append(candidates, infer_candidate{params: c,
				describe: fmt.Sprintf("lnglat %s,%s, attr %s", c.LngLat[0], c.LngLat[1], c.Attr)})
			if len(candidates) >= 6 {
				return candidates, nil
			}
		}
	}

	return candidates, nil
}

// best_pairs ranks candidate column pairs by how many of want they reached,
// keeping at most n of them in the profile's order.
func best_pairs(pairs [][2]int, scores []int, want int, n int) []int {
	type scored struct {
		i, score int
	}

	ranked := []scored{}
	for i, s := range scores {
		if s >= want {
			ranked = append(ranked, scored{i, s})
		}
	}

	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })

	out := []int{}
	for _, r := range ranked {
		out = append(out, r.i)
		if len(out) == n {
			break
		}
	}

	return out
}

// csv_profile is what the first rows say about the columns: which parse as
// numbers, and the range each covers.
type csv_profile struct {
	header   []string
	numeric  []bool
	integral []bool
	lo, hi   []float64
	rows     int
}

const (
	csv_profile_rows = 2000
	csv_scan_rows    = 200000
)

func csv_profile_read(f filename) (*csv_profile, error) {
	file, err := os.Open(f)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	r := stdcsv.NewReader(bufio.NewReaderSize(file, 1<<20))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	header, err := r.Read()
	if err != nil {
		return nil, err
	}

	// A wide csv is bounded: only its first 80 columns are considered.
	if len(header) > 80 {
		header = header[:80]
	}

	n := len(header)

	p := &csv_profile{
		header:   header,
		numeric:  make([]bool, n),
		integral: make([]bool, n),
		lo:       make([]float64, n),
		hi:       make([]float64, n),
	}

	for i := range p.numeric {
		p.numeric[i] = true
		p.integral[i] = true
	}

	for p.rows < csv_profile_rows {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		nonempty := false
		for i := 0; i < n && i < len(row); i++ {
			v, err := strconv.ParseFloat(strings.TrimSpace(row[i]), 64)
			if err != nil {
				p.numeric[i] = false
				continue
			}

			if p.rows == 0 {
				p.lo[i], p.hi[i] = v, v
			} else {
				p.lo[i] = math.Min(p.lo[i], v)
				p.hi[i] = math.Max(p.hi[i], v)
			}

			if v != math.Trunc(v) {
				p.integral[i] = false
			}

			nonempty = true
		}

		if nonempty {
			p.rows++
		}
	}

	return p, nil
}

// lnglat_pairs is the column pairs the sampled rows say could be a longitude
// and a latitude, in the order the csv names them.
func (p *csv_profile) lnglat_pairs() [][2]int {
	pairs := [][2]int{}
	for a := 0; a < len(p.header); a++ {
		if !p.numeric[a] || p.lo[a] < -180 || p.hi[a] > 180 {
			continue
		}

		for b := 0; b < len(p.header); b++ {
			if a == b || !p.numeric[b] || p.lo[b] < -90 || p.hi[b] > 90 {
				continue
			}

			pairs = append(pairs, [2]int{a, b})
		}
	}

	return pairs
}

// attr_columns is the numeric columns whose sampled values the raster holds,
// whole numbers first: a burn into an Int16 grid truncates.
func (p *csv_profile) attr_columns(values map[int64]bool) []int {
	type scored struct {
		i, hits int
	}

	ranked := []scored{}
	for i := range p.header {
		if !p.numeric[i] || !p.integral[i] {
			continue
		}
		if p.lo[i] < math.MinInt16 || p.hi[i] > math.MaxInt16 {
			continue
		}

		hits := 0
		if values[int64(p.lo[i])] {
			hits++
		}
		if values[int64(p.hi[i])] {
			hits++
		}

		if hits > 0 {
			ranked = append(ranked, scored{i, hits})
		}
	}

	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].hits > ranked[b].hits })

	out := []int{}
	for _, r := range ranked {
		out = append(out, r.i)
		if len(out) == 4 {
			break
		}
	}

	return out
}

// csv_pair_points counts, for every column pair, how many of the published
// points one of the file's rows reproduces.
func csv_pair_points(f filename, pairs [][2]int, points [][2]float64, rows int) ([]int, error) {
	index := map[[2]int64]int{}
	for j, pt := range points {
		index[csv_key(pt[0], pt[1])] = j
	}

	scores := make([]int, len(pairs))
	found := make([]map[int]bool, len(pairs))
	for i := range found {
		found[i] = map[int]bool{}
	}

	err := csv_scan(f, rows, func(row []string) bool {
		done := true
		for i, pair := range pairs {
			a, ok := csv_float(row, pair[0])
			b, ok2 := csv_float(row, pair[1])
			if ok && ok2 {
				if j, hit := index[csv_key(a, b)]; hit && !found[i][j] {
					found[i][j] = true
					scores[i]++
				}
			}

			if scores[i] < len(points) {
				done = false
			}
		}

		return done
	})

	return scores, err
}

// csv_pair_bounds counts, for every column pair, the sampled rows that land
// inside box.
func csv_pair_bounds(f filename, pairs [][2]int, box [4]float64, rows int) ([]int, error) {
	scores := make([]int, len(pairs))

	err := csv_scan(f, rows, func(row []string) bool {
		for i, pair := range pairs {
			a, ok := csv_float(row, pair[0])
			b, ok2 := csv_float(row, pair[1])
			if ok && ok2 && a >= box[0] && a <= box[2] && b >= box[1] && b <= box[3] {
				scores[i]++
			}
		}

		return false
	})

	return scores, err
}

// csv_scan calls row for each record, up to rows, until it answers true.
func csv_scan(f filename, rows int, row func([]string) bool) error {
	file, err := os.Open(f)
	if err != nil {
		return err
	}
	defer file.Close()

	r := stdcsv.NewReader(bufio.NewReaderSize(file, 1<<20))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	if _, err := r.Read(); err != nil {
		return err
	}

	for n := 0; n < rows; n++ {
		record, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return nil
		}

		if row(record) {
			return nil
		}
	}

	return nil
}

func csv_float(row []string, i int) (float64, bool) {
	if i >= len(row) {
		return 0, false
	}

	v, err := strconv.ParseFloat(strings.TrimSpace(row[i]), 64)

	return v, err == nil
}

func csv_key(lng, lat float64) [2]int64 {
	return [2]int64{int64(math.Round(lng * 1e7)), int64(math.Round(lat * 1e7))}
}

// geojson_points is the published GeoJSON's point coordinates, up to a sample
// large enough to tell a pair of columns apart.
func geojson_points(f filename) ([][2]float64, error) {
	if f == "" {
		return nil, errors.New("no published vectors")
	}

	stream, err := open_features(f)
	if err != nil {
		return nil, err
	}
	defer stream.close()

	const sample = 40

	points := [][2]float64{}

	for len(points) < sample {
		raw, err := stream.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		var feat struct {
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		}
		if err := json.Unmarshal(raw, &feat); err != nil {
			return nil, err
		}

		if feat.Geometry.Type != "Point" {
			continue
		}

		var c []float64
		if err := json.Unmarshal(feat.Geometry.Coordinates, &c); err != nil || len(c) < 2 {
			continue
		}

		points = append(points, [2]float64{c[0], c[1]})
	}

	return points, nil
}

// raster_bounds_4326 is the raster's extent in the coordinates a csv of
// longitudes and latitudes would use.
func raster_bounds_4326(f filename) ([4]float64, error) {
	ds, err := gdal.Open(f, gdal.ReadOnly)
	if err != nil {
		return [4]float64{}, err
	}
	defer ds.Close()

	gt := ds.GeoTransform()
	w, h := float64(ds.RasterXSize()), float64(ds.RasterYSize())

	corners := [][2]float64{
		{gt[0], gt[3]},
		{gt[0] + w*gt[1], gt[3]},
		{gt[0], gt[3] + h*gt[5]},
		{gt[0] + w*gt[1], gt[3] + h*gt[5]},
	}

	projected, err := proj_transform(ds.Projection(), "EPSG:4326", corners)
	if err != nil {
		return [4]float64{}, err
	}

	box := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, c := range projected {
		box[0] = math.Min(box[0], c[0])
		box[1] = math.Min(box[1], c[1])
		box[2] = math.Max(box[2], c[0])
		box[3] = math.Max(box[3], c[1])
	}

	return box, nil
}

// proj_transform reprojects points through gdaltransform, so the csv needs no
// projection of its own.
func proj_transform(from, to string, points [][2]float64) ([][2]float64, error) {
	cmd := exec.Command("gdaltransform", "-s_srs", from, "-t_srs", to)

	in := strings.Builder{}
	for _, p := range points {
		fmt.Fprintf(&in, "%s %s\n", strconv.FormatFloat(p[0], 'g', -1, 64), strconv.FormatFloat(p[1], 'g', -1, 64))
	}
	cmd.Stdin = strings.NewReader(in.String())

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gdaltransform: %w", err)
	}

	result := [][2]float64{}
	sc := bufio.NewScanner(bytes.NewReader(out))

	for sc.Scan() {
		var x, y float64
		if _, err := fmt.Sscanf(sc.Text(), "%f %f", &x, &y); err != nil {
			continue
		}
		result = append(result, [2]float64{x, y})
	}

	if len(result) != len(points) {
		return nil, fmt.Errorf("gdaltransform returned %d points for %d", len(result), len(points))
	}

	return result, nil
}

// raster_values is the set of values the published raster holds, from strips
// spread through it: enough to recognise the column it was burned from.
func raster_values(f filename) (map[int64]bool, error) {
	ds, err := gdal.Open(f, gdal.ReadOnly)
	if err != nil {
		return nil, err
	}
	defer ds.Close()

	values := map[int64]bool{}

	s := strips_of(ds)
	band := ds.RasterBand(1)
	nodata, has := band.NoDataValue()

	const samples = 4
	step := max(1, ds.RasterYSize()/samples)

	for y := 0; y < ds.RasterYSize(); y += step {
		n := min(64, ds.RasterYSize()-y)
		rows, err := s.rows(y, y+n-1)
		if err != nil {
			return nil, err
		}

		for _, v := range rows {
			if math.IsNaN(v) || (has && v == nodata) {
				continue
			}
			values[int64(math.Round(v))] = true
			if len(values) > 4096 {
				return values, nil
			}
		}
	}

	return values, nil
}

func infer_read_jobs(f filename) ([]infer_job, error) {
	file, err := os.Open(f)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var jobs []infer_job
	seen := map[string]bool{}

	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	for n := 1; sc.Scan(); n++ {
		l := strings.TrimSpace(sc.Text())
		if l == "" {
			continue
		}

		var j infer_job
		if err := json.Unmarshal([]byte(l), &j); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", f, n, err)
		}

		if j.Dataset == "" || j.Routine == "" || len(j.Outputs) == 0 {
			return nil, fmt.Errorf("%s:%d: job without a dataset, a routine or published outputs", f, n)
		}

		if _, ok := infer_routines[j.Routine]; !ok {
			return nil, fmt.Errorf("%s:%d: unknown routine %s", f, n, j.Routine)
		}

		if !seen[j.Dataset] {
			seen[j.Dataset] = true
			jobs = append(jobs, j)
		}
	}

	return jobs, sc.Err()
}

func infer_manifest_read(f filename) (map[string]infer_entry, error) {
	entries := map[string]infer_entry{}

	file, err := os.Open(f)
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	for sc.Scan() {
		var e infer_entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		entries[e.Dataset] = e
	}

	return entries, sc.Err()
}

func infer_summary(entries map[string]infer_entry) {
	by := map[string]map[string]int{}

	for _, e := range entries {
		if by[e.Routine] == nil {
			by[e.Routine] = map[string]int{}
		}
		by[e.Routine][e.Check.Status]++
	}

	routines := make([]string, 0, len(by))
	for r := range by {
		routines = append(routines, r)
	}
	sort.Strings(routines)

	for _, r := range routines {
		c := by[r]
		fmt.Printf("%s: reproduced %d, equivalent %d, differs %d, failed %d, unchecked %d\n",
			r, c[infer_reproduced], c[infer_equivalent], c[infer_differs], c[infer_failed], c[infer_unchecked])
	}

	unsure := []infer_entry{}
	for _, e := range entries {
		if e.Check.Status == infer_differs || e.Check.Status == infer_failed {
			unsure = append(unsure, e)
		}
	}

	sort.Slice(unsure, func(a, b int) bool { return unsure[a].Dataset < unsure[b].Dataset })

	for _, e := range unsure {
		fmt.Printf("%-9s %-16s %s %s\n", e.Check.Status, e.Routine, e.Dataset, e.Check.Note)
	}
}
