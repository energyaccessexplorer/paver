import '/lib/selectlist.js';

const origin = "https://api.energyaccessexplorer.org"

const form = document.querySelector('#not-a-form-form');
const header = document.querySelector('h3');
const instructions = document.querySelector('h4');
const info = document.querySelector('pre');

const payload = {
	geographyid: null,
	datasetid: null,
	dataseturl: null,
	boundaryurl: null,
	referenceurl: null,
	attrs: "name",
};

async function geographiesinput() {
	const geos = await fetch(origin + "/geographies?select=id,name,cca3,boundary(id,endpoint)&boundary_file=not.is.null")
				.then(r => r.json());

	const sl = new selectlist(
		"geographies",
		geos.reduce((a,c) => {
		  a[c['cca3']] = c['name'];
		  return a;
		}, {})
	);

	form.append(sl.input);

	sl.input.addEventListener('change', function(e) {
		const geo = geos.find(x => x['cca3'] === this.value);

		payload['geographyid'] = geo['id'];
		payload['boundaryurl'] = geo['boundary']['endpoint'];

		datasetidinput(this);
	});

	sl.input.focus();

	instructions.innerText = "Pick a geography";

	info.innerText = "If a geography is no on the list, it probably means it does not have a boundary_file set";
};

async function datasetidinput(oldinput) {
	const datas = await fetch(origin + `/datasets?select=id,name,category_name&geography_id=eq.${payload['geographyid']}`)
				.then(r => r.json());

	const sl = new selectlist(
		"datasets",
		datas.reduce((a,c) => {
		  a[c['id']] = (c['name'] ? c['name'] : c['category_name']);
		  return a;
		}, {})
	);

	oldinput.remove();
	form.prepend(sl.input);

	sl.input.addEventListener('change', function(e) {
		payload['datasetid'] = this.value;
		datasetinput(this);
	});

	sl.input.focus();

	instructions.innerText = "Pick a dataset";

	info.innerText = "";
};

function datasetinput(oldinput) {
	return locationinput(
		oldinput,
		'dataseturl',
		'What dataset are we working with?',
		targetinput);
};

function targetinput(oldinput) {
	return locationinput(
		oldinput,
		'boundaryurl',
		"The dataset we will use to clip the previous dataset (generally a GEOJSON)",
		referenceinput);
};

function referenceinput(oldinput) {
	return locationinput(
		oldinput,
		'referenceurl',
		"The dataset we will use to generate the rasterized version (generally a SHP)",
		submit);
};

async function locationinput(oldinput, label, infotext = "", followup) {
	const input = document.createElement('input');
	input.setAttribute('required', '');
	input.setAttribute('type', 'url');
	input.setAttribute('name', 'location');
	input.setAttribute('autocomplete', 'off');

	input.value = "https://wri-public-data.s3.amazonaws.com/EnergyAccess/";

	oldinput.remove();
	form.prepend(input);

	input.focus();

	input.addEventListener('change', async function(e) {
		const response = await fetch(this.value, {
		  method: "HEAD"
		}).catch(err => {
		  info.innerText = err + "\n(probably a CORS error, check the console log in the developer tools)";
		});

		infoerror(response);

		if (response.ok) {
		  payload[label] = this.value;
			followup(input);
		}
	});

	instructions.innerText = "Give a URL go to get the file";

	info.innerText = infotext;
};

async function submit() {
	var body = [];

	for (var p in payload)
		body.push(encodeURIComponent(p) +
							"=" +
							encodeURIComponent(payload[p]));


	const response = await fetch('/routines?routine=vectors_clip_proximity', {
		method: 'POST',
		headers: {
			'Content-Type': 'application/x-www-form-urlencoded'
		},
		body: body.join("&"),
	});

	infoerror(response);
};

function infoerror(response) {
	if (!response.ok) {
		info.innerText = `${response.status} - ${response.statusText}
${JSON.stringify(response, null, 2)}`;
	}
};

export function start() {
	header.innerText = "Vectors/Proximity";
	geographiesinput();
};
