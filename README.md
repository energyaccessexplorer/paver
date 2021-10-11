# Energy Access Explorer Paver

Paver is the EAE's backend for processing the geolocated data needed for the
EAE's [tool](https://github.com/energyaccessexplorer/tool)


## Building & Hacking

Dependencies:

- Go (> 1.16)
- GDAL (>= 3.3)
- S3-compatible bucket
- JWT authentication

To get started, edit the `.env` to your needs and run (`bmake` in Linux)

	$ make

**Important**: when compiling the executable the S3 credentials and the JWT key
are burnt into the binary file. Do **NOT** expect them to be obscured - compile
and deploy to trustworthy locations, ok?


## License

This project is licensed under MIT. Additionally, you must read the
[attribution page](https://www.energyaccessexplorer.org/attribution)
before using any part of this project.
