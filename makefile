include .env

default: clean build server

build:
	go fmt
	CGO_LDFLAGS="-L/usr/local/lib -lgdal" \
	CGO_CFLAGS="-I/usr/local/include" \
	go build

clean:
	-rm -f paver
	-rm -f ./outputs/*

server:
	@rm -rf /tmp/paver-server.sock
	@./paver -server -jwtkey 1234 -role admin -tmpdir /tmp -dir /tmp

cli:
	@./paver -cli -c admin_boundaries \
		-i ${POLYGON_SHP} \
		-g DistrictID

	@./paver -cli -c vectors_routine \
		-i ${LINES_SHP} \
		-r ${POLYGON_SHP} \
		-t ${POLYGON_GEOJSON} \
		-g full_id

	@./paver -cli -c vectors_clipped_routine \
		-i ${POINTS_GEOJSON} \
		-r ${POLYGON_SHP} \
		-t ${POLYGON_GEOJSON} \
		-g iso

	@./paver -cli -c csv \
		-i ${POLYGON_SHP} \
		-s DistrictID \
		-s Radio

	@ls -lh outputs

all: clean build
