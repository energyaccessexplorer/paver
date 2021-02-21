import '/lib/selectlist.js';

import * as inputs from '/inputs.js';

const header = document.querySelector('header');

window.payload = {
	geographyid: null,
	datasetid: null,
	dataseturl: null,
	boundaryurl: null,
	referenceurl: null,
	attrs: "name",
};

function datasetid(oldinput) {
	return inputs.datasetid({
		before: _ => oldinput.remove(),
		after: t => datasetinput(t)
	});
};

function datasetinput(oldinput) {
	return inputs.url({
		label: 'dataseturl',
		info: 'What dataset are we working with? (GEOJSON)',
		before: _ => oldinput.remove(),
		after: targetinput,
	});
};

function targetinput(oldinput) {
	return inputs.url({
		label: 'boundaryurl',
		info: "The dataset we will use to clip the previous dataset (generally a GEOJSON)",
		before: _ => oldinput.remove(),
		after: referenceinput,
	});
};

function referenceinput(oldinput) {
	return inputs.url({
		label: 'referenceurl',
		info: "The dataset we will use to generate the rasterized version (generally a SHP)",
		before: _ => oldinput.remove(),
		after: submit,
	});
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

	inputs.infoerror(response);
};

export function start() {
	header.innerText = "Vectors/Proximity";

	inputs.geographies({
		after: x => datasetid(x)
	});
};
