include .env

CMD = paver -server -tmpdir /tmp -dir /tmp -role admin -role master -jwtkey ${JWTKEY}

default: clean build server

build:
	go fmt
	CGO_LDFLAGS="-L/usr/local/lib -lgdal" \
	CGO_CFLAGS="-I/usr/local/include" \
	go build -ldflags "-s \
		-X main.S3KEY=${S3KEY} \
		-X main.S3SECRET=${S3SECRET} \
		-X main.S3PROVIDER=${S3PROVIDER} \
		-X main.S3BUCKET=${S3BUCKET} \
		-X main.S3DIRECTORY=${S3DIRECTORY} \
		-X main.S3ACL=${S3ACL}"

clean:
	-rm -f paver
	-rm -f ./outputs/*

server:
	@rm -rf /tmp/paver-server.sock
	@./${CMD}

cli:
	@./paver -cli -c admin_boundaries \
		-i ${POLYGON_SHP} \
		-g DistrictID

	@./paver -cli -c vectors_routine \
		-i ${LINES_SHP} \
		-r ${POLYGON_GEOJSON} \
		-g full_id

	@./paver -cli -c vectors_clipped_routine \
		-i ${POINTS_GEOJSON} \
		-r ${POLYGON_GEOJSON} \
		-g iso

	@./paver -cli -c csv \
		-i ${POLYGON_SHP} \
		-s DistrictID \
		-s Radio

	@ls -lh outputs

all: clean build
