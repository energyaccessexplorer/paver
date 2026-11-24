package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type s3config struct {
	Name      string `json:"name"`
	Key       string `json:"key"`
	Secret    string `json:"secret"`
	Provider  string `json:"provider"`
	Bucket    string `json:"bucket"`
	Directory string `json:"directory"`
	ACL       string `json:"acl"`
	Region    string `json:"region"`

	// Providers that speak the S3 protocol somewhere other than AWS: Endpoint
	// is the API base requests are signed for, Public the base of the object's
	// public URL.
	Endpoint string `json:"endpoint"`
	Public   string `json:"public"`

	// With a service key, a supabase bucket is written through the Storage
	// API (its base, e.g. https://<ref>.supabase.co/storage/v1) instead of
	// presigned S3 PUTs.
	Storage    string `json:"storage"`
	ServiceKey string `json:"service_key"`

	// Contents are disposable, so repave may fill the bucket from any source
	// without a flag (see repave_main and repave_one).
	Scratch bool `json:"scratch"`
}

func s3config_get(name string) (_ s3config, err error) {
	j, err := os.ReadFile(buckets)
	if err != nil {
		return
	}

	var array []s3config

	if err = json.Unmarshal(j, &array); err != nil {
		return
	}

	for _, e := range array {
		if e.Name == name {
			return e, nil
		}
	}

	err = errors.New("bucket config not found: " + name)

	return
}

// A "local" bucket is a directory: Directory is where objects are written and
// Bucket the URL they are served from.
const local_provider = "local"

// A "supabase" bucket is Supabase Storage's S3 protocol, which addresses
// buckets path-style: the object key is Directory joined to key.
const supabase_provider = "supabase"

func (s3 s3config) scratch() bool {
	return s3.Provider == local_provider || s3.Scratch
}

// s3key is the object's key within a path-style bucket: the directory prefix,
// when there is one, joined to key.
func s3key(s3 s3config, key string) string {
	if s3.Directory == "" {
		return key
	}

	return strings.Trim(s3.Directory, "/") + "/" + key
}

func s3endpoint(s3 s3config, key string) string {
	switch s3.Provider {
	case local_provider:
		return strings.TrimRight(s3.Bucket, "/") + "/" + key
	case supabase_provider:
		return fmt.Sprintf("%s/%s/%s", strings.TrimRight(s3.Public, "/"), s3.Bucket, s3key(s3, key))
	}

	return fmt.Sprintf("https://%s.%s/%s/%s", s3.Bucket, s3.Provider, s3.Directory, key)
}

func localput(fname filename, key string, s3 s3config) bool {
	if err := os.MkdirAll(s3.Directory, 0755); err != nil {
		logger.Println(err.Error())
		return false
	}

	content, err := os.ReadFile(fname)
	if err != nil {
		logger.Println(err.Error())
		return false
	}

	if err := os.WriteFile(filepath.Join(s3.Directory, key), content, 0644); err != nil {
		logger.Println(err.Error())
		return false
	}

	return true
}

// object_key names a paver output by its uuid and what the file is.
func object_key(fname filename) string {
	ext, _ := file_kind(fname, "")
	return _uuid(fname) + ext
}

func s3put_as(fname filename, key string, s3 s3config) bool {
	return s3put_cached(fname, key, s3, upload_cache_control)
}

func s3put_cached(fname filename, key string, s3 s3config, cache string) bool {
	if s3.Provider == local_provider {
		return localput(fname, key, s3)
	}

	if s3.Provider == supabase_provider && s3.ServiceKey != "" {
		return storageput(fname, key, s3, cache)
	}

	content, err := os.ReadFile(fname)
	if err != nil {
		return false
	}

	endpoint, err := s3presigned(s3, "PUT", key)
	if err != nil {
		return false
	}

	client := &http.Client{}

	q, err := http.NewRequest("PUT", endpoint, bytes.NewReader(content))
	if err != nil {
		return false
	}

	q.Header.Set("Content-Type", key_type(key, fname))
	q.Header.Set("Cache-Control", cache)

	r, err := client.Do(q)
	if err != nil {
		return false
	}

	c, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Println(r.Status, err.Error(), c)
		return false
	}

	if r.StatusCode > 399 {
		logger.Println(r.Status, c)
		return false
	}

	return true
}

// Objects are named by content hash or a fresh uuid, so they never change
// under the same name: they can be cached at the edge and in browsers for a
// year, with no invalidation.
const upload_cache_control = "public, max-age=31536000, immutable"

// An object the apps name by path can be replaced under the same name.
const named_cache_control = "public, max-age=3600"

func storageput(fname filename, key string, s3 s3config, cache string) bool {
	file, err := os.Open(fname)
	if err != nil {
		logger.Println(err.Error())
		return false
	}
	defer file.Close()

	st, err := file.Stat()
	if err != nil {
		logger.Println(err.Error())
		return false
	}

	if st.Size() > tus_threshold {
		return storageput_resumable(file, st.Size(), key, s3, cache)
	}

	u := fmt.Sprintf("%s/object/%s/%s", strings.TrimRight(s3.Storage, "/"), s3.Bucket, s3key(s3, key))

	q, err := http.NewRequest("POST", u, file)
	if err != nil {
		logger.Println(err.Error())
		return false
	}

	q.ContentLength = st.Size()
	q.Header.Set("apikey", s3.ServiceKey)
	q.Header.Set("Authorization", "Bearer "+s3.ServiceKey)
	q.Header.Set("Content-Type", key_type(key, fname))
	q.Header.Set("x-upsert", "true")
	q.Header.Set("Cache-Control", cache)

	r, err := http_client.Do(q)
	if err != nil {
		logger.Println(err.Error())
		return false
	}
	defer r.Body.Close()

	if r.StatusCode > 299 {
		b, _ := io.ReadAll(r.Body)
		logger.Println("POST", u, r.Status, string(b))
		return false
	}

	return true
}

// Storage's resumable uploads take chunks of exactly this size, the last
// excepted.
const tus_chunk = 6 << 20

// Larger files are uploaded resumably. Smaller ones go in one POST, which is
// the only upload that stores a Cache-Control as given: a resumable one keeps
// just its max-age.
const tus_threshold = 64 << 20

const tus_attempts = 8

// storageput_resumable uploads through Storage's TUS endpoint, a chunk per
// request: a single POST of a large file outlasts the client's timeout, and
// one over 5GB is refused at the CDN whatever the bucket allows.
func storageput_resumable(file *os.File, size int64, key string, s3 s3config, cache string) bool {
	endpoint := strings.TrimRight(s3.Storage, "/") + "/upload/resumable"

	meta := []string{}
	for _, kv := range [][2]string{
		{"bucketName", s3.Bucket},
		{"objectName", s3key(s3, key)},
		{"contentType", key_type(key, file.Name())},
		{"cacheControl", cache_max_age(cache)},
	} {
		meta = append(meta, kv[0]+" "+base64.StdEncoding.EncodeToString([]byte(kv[1])))
	}

	// Storage answers bursts with 429 and the odd 5xx; every request is retried.
	retry := func(what string, try func() (bool, error)) bool {
		for attempt := 1; ; attempt++ {
			ok, err := try()
			if ok {
				return true
			}
			if attempt == tus_attempts {
				logger.Println("resumable upload:", what, err)
				return false
			}
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}

	var location string
	created := retry("create "+key, func() (bool, error) {
		q, err := http.NewRequest("POST", endpoint, nil)
		if err != nil {
			return false, err
		}
		q.Header.Set("apikey", s3.ServiceKey)
		q.Header.Set("Authorization", "Bearer "+s3.ServiceKey)
		q.Header.Set("Tus-Resumable", "1.0.0")
		q.Header.Set("Upload-Length", strconv.FormatInt(size, 10))
		q.Header.Set("Upload-Metadata", strings.Join(meta, ","))
		q.Header.Set("x-upsert", "true")

		r, err := http_client.Do(q)
		if err != nil {
			return false, err
		}
		defer r.Body.Close()

		if r.StatusCode != http.StatusCreated {
			b, _ := io.ReadAll(r.Body)
			return false, fmt.Errorf("%s %s", r.Status, b)
		}

		base, _ := url.Parse(endpoint)
		loc, err := base.Parse(r.Header.Get("Location"))
		if err != nil {
			return false, err
		}
		location = loc.String()
		return true, nil
	})
	if !created {
		return false
	}

	// Where the server says the upload is: a chunk that failed may have landed.
	offset_now := func() (int64, error) {
		q, err := http.NewRequest("HEAD", location, nil)
		if err != nil {
			return 0, err
		}
		q.Header.Set("apikey", s3.ServiceKey)
		q.Header.Set("Authorization", "Bearer "+s3.ServiceKey)
		q.Header.Set("Tus-Resumable", "1.0.0")

		r, err := http_client.Do(q)
		if err != nil {
			return 0, err
		}
		r.Body.Close()
		return strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	}

	for offset := int64(0); offset < size; {
		n := min(tus_chunk, size-offset)

		sent := retry(fmt.Sprintf("%s at %d", key, offset), func() (bool, error) {
			q, err := http.NewRequest("PATCH", location, io.NewSectionReader(file, offset, n))
			if err != nil {
				return false, err
			}
			q.ContentLength = n
			q.Header.Set("apikey", s3.ServiceKey)
			q.Header.Set("Authorization", "Bearer "+s3.ServiceKey)
			q.Header.Set("Tus-Resumable", "1.0.0")
			q.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
			q.Header.Set("Content-Type", "application/offset+octet-stream")

			r, err := http_client.Do(q)
			if err == nil {
				defer r.Body.Close()
				if r.StatusCode == http.StatusNoContent {
					o, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
					if err == nil {
						offset = o
					}
					return err == nil, err
				}
				b, _ := io.ReadAll(r.Body)
				err = fmt.Errorf("%s %s", r.Status, b)
			}
			if o, e := offset_now(); e == nil && o > offset {
				offset = o
				return true, nil
			}
			return false, err
		})
		if !sent {
			return false
		}
	}

	return true
}

// cache_max_age is a Cache-Control's max-age in seconds, the form Storage's
// resumable uploads take it in.
func cache_max_age(cache string) string {
	for _, d := range strings.Split(cache, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(d), "max-age="); ok {
			return v
		}
	}
	return "0"
}

// s3has says the bucket holds key with size bytes. A PUT is atomic, so an
// object of the right size is a whole one.
func s3has(s3 s3config, key string, size int64) bool {
	if s3.Provider == local_provider {
		st, err := os.Stat(filepath.Join(s3.Directory, key))
		return err == nil && st.Size() == size
	}

	r, err := http_client.Head(s3endpoint(s3, key))
	if err != nil {
		return false
	}
	r.Body.Close()

	return r.StatusCode == http.StatusOK && r.ContentLength == size
}

// s3presigned_by_name presigns for a bucket named in the buckets file, taking
// the object's prefix from the caller rather than from the entry.
func s3presigned_by_name(platform string, method string, directory string, filename string) (string, error) {
	conf, err := s3config_get(platform)
	if err != nil {
		return "", err
	}

	conf.Directory = directory

	return s3presigned(conf, method, filename)
}

func s3presigned(conf s3config, method string, key string) (url string, err error) {
	ctx, cancelFn := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancelFn()

	c, err := config.LoadDefaultConfig(
		context.TODO(),
		config.WithRegion(conf.Region),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				conf.Key,
				conf.Secret,
				"", // session token
			),
		),
	)
	if err != nil {
		return
	}

	cli := s3.NewPresignClient(s3.NewFromConfig(c, func(o *s3.Options) {
		if conf.Provider == supabase_provider {
			o.BaseEndpoint = aws.String(conf.Endpoint)
			o.UsePathStyle = true
		}
	}))

	var request *v4.PresignedHTTPRequest
	if method == "GET" {
		request, err = cli.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(conf.Bucket),
			Key:    aws.String(s3key(conf, key)),
		}, s3.WithPresignExpires(10*time.Minute))
	} else if method == "PUT" {
		request, err = cli.PresignPutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(conf.Bucket),
			Key:    aws.String(s3key(conf, key)),
		}, s3.WithPresignExpires(10*time.Minute))
	} else {
		err = errors.New("Parameter 'method' is incorrect")
		return
	}

	return request.URL, err
}
