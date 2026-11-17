package main

import (
	"bytes"
	"context"
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
	"os"
	"path/filepath"
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

func (p routine_params) url(fname filename) string {
	return s3endpoint(p.S3, _uuid(fname))
}

func s3put(fname filename, s3 s3config) bool {
	return s3put_as(fname, _uuid(fname), s3)
}

func s3put_as(fname filename, key string, s3 s3config) bool {
	if s3.Provider == local_provider {
		return localput(fname, key, s3)
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
