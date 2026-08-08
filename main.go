package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"github.com/satori/go.uuid"
	"log"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

var UUID_REGEXP = regexp.MustCompile("[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}")

var COMMIT_SHA string

var (
	run_server = true
	pubkeyfile string
	tmpdir     string
	socket     string
	buckets    string
)

var (
	logfile     *os.File
	logfilename string
	logger      *log.Logger
)

type filename = string

func main() {
	parse_flags()

	capture_setup()

	// Abort stalled HTTP transfers: GDAL would otherwise wait forever,
	// wedging the job (and its concurrency slot) with it.
	if os.Getenv("GDAL_HTTP_LOW_SPEED_TIME") == "" {
		os.Setenv("GDAL_HTTP_LOW_SPEED_TIME", "60")
	}
	if os.Getenv("GDAL_HTTP_LOW_SPEED_LIMIT") == "" {
		os.Setenv("GDAL_HTTP_LOW_SPEED_LIMIT", "1024")
	}

	// Threads for GTiff block compression and warp kernels.
	if os.Getenv("GDAL_NUM_THREADS") == "" {
		os.Setenv("GDAL_NUM_THREADS", "ALL_CPUS")
	}

	routine_slots = make(chan struct{}, max_concurrent)

	logger_setup()

	serve()
}

func parse_flags() {
	flag.StringVar(&pubkeyfile, "pubkey", "", "Public key file to check JWTs")
	flag.StringVar(&socket, "socket", "/tmp/paver-server.sock", "Socket file to run on")
	flag.StringVar(&logfilename, "log", "/tmp/paver.log", "")
	flag.StringVar(&tmpdir, "tmpdir", "/tmp", "")
	flag.StringVar(&buckets, "buckets", "/etc/paver-buckets.json", "")
	flag.IntVar(&max_concurrent, "max-concurrent", runtime.NumCPU(), "Maximum number of routines to run concurrently")
	flag.DurationVar(&queue_wait, "queue-wait", 10*time.Second, "How long to wait for a free concurrency slot before returning 503")

	flag.Parse()
}

func logger_setup() {
	var err error

	logfile, err = os.OpenFile(logfilename, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		panic(err)
	}

	var logbuf bytes.Buffer
	logger = log.New(&logbuf, "", log.LstdFlags)
	logger.SetOutput(logfile)

	println("Logging to:", logfilename)
}

func _filename() filename {
	return tmpdir + "/" + uuid.NewV4().String()
}

func _uuid(s string) string {
	return fmt.Sprintf("%s", UUID_REGEXP.Find([]byte(s)))
}

func trash(files ...filename) {
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			logger.Println(err.Error())
		}
	}
}

// Redirect fd 2 once: concurrent per-call redirection deadlocks (captures
// steal each other's fd 2 and the pipe never reaches EOF). The reader keeps
// the last captureKeep lines for capture() and tees to the original stderr.
var (
	captureMu    sync.Mutex
	captureLines []string
	captureTotal int
)

const captureKeep = 1000

func capture_setup() {
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}

	ostderr, err := syscall.Dup(syscall.Stderr)
	if err != nil {
		panic(err)
	}
	if err := syscall.Dup2(int(w.Fd()), syscall.Stderr); err != nil {
		panic(err)
	}

	go func() {
		out := os.NewFile(uintptr(ostderr), "stderr")
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 1<<20)

		for sc.Scan() {
			line := sc.Text()

			captureMu.Lock()
			captureLines = append(captureLines, line)
			captureTotal++
			if len(captureLines) > captureKeep {
				captureLines = captureLines[len(captureLines)-captureKeep/2:]
			}
			captureMu.Unlock()

			fmt.Fprintln(out, line)
		}
	}()
}

// capture returns a function yielding stderr lines produced since the call.
func capture() func() string {
	captureMu.Lock()
	start := captureTotal
	captureMu.Unlock()

	return func() string {
		captureMu.Lock()
		defer captureMu.Unlock()

		from := len(captureLines) - (captureTotal - start)
		if from < 0 {
			from = 0
		}

		return strings.Join(captureLines[from:], "\n")
	}
}
