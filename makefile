build:
	go fmt
	CGO_LDFLAGS="-L/usr/local/lib -lgdal" \
	CGO_CFLAGS="-I/usr/local/include" \
	go build

clean:
	-rm -f paver

all: clean build
