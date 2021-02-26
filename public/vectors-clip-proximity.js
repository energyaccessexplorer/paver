import '/lib/selectlist.js';

import * as inputs from '/inputs.js';

import {socketlisten} from '/socket.js';

const header = document.querySelector('header');

window.payload = {
	geographyid: null,
	datasetid: null,
	dataseturl: null,
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
		after: submit,
	});
};

async function submit() {
	var body = [];

	const loading = document.querySelector('tb-loading');
	const infopre = document.querySelector('pre');

	for (var p in payload)
		body.push(encodeURIComponent(p) +
							"=" +
							encodeURIComponent(payload[p]));


	loading.style.display = 'block';
	infopre.innerText = "";

	socketlisten(m => infopre.innerText += "\n" + m);

	const response = await fetch('/routines?routine=vectors_clip_proximity', {
		method: 'POST',
		headers: {
			'Content-Type': 'application/x-www-form-urlencoded'
		},
		body: body.join("&"),
	});

	inputs.infoerror(response);

	loading.style.display = '';
};

export function start() {
	header.innerText = "Vectors/Proximity";

	inputs.geographies({
		after: x => datasetid(x)
	});
};
