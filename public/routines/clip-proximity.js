import '/lib/selectlist.js';

import * as inputs from '../inputs.js';

import {socketlisten} from '../socket.js';

const header = document.querySelector('header');
header.innerText = "Clip/Proximity";

const payload = {
	geographyid: null,
	datasetid: null,
	dataseturl: null,
	referenceurl: null,
	attrs: "name",
};

inputs.geographies({
	after: x => datasetid(x),
	payload
});

function datasetid(oldinput) {
	return inputs.datasetid({
		before: _ => oldinput.remove(),
		after: t => datasetinput(t),
		payload
	});
};

function datasetinput(oldinput) {
	return inputs.url({
		label: 'dataseturl',
		info: 'What dataset are we working with? (GEOJSON)',
		before: _ => oldinput.remove(),
		after: submit,
		payload
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