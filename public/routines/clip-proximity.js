import '../lib/selectlist.js';

import * as inputs from '../inputs.js';

import * as main from './main.js';

const header  = "Clip/Proximity";

const payload = {
	geographyid: null,
	datasetid: null,
	dataseturl: null,
	referenceurl: null,
	attrs: null,
};

main.setup({ header, payload });

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
		after: t => attrsinput(t),
		payload
	});
};

function attrsinput(oldinput) {
	return inputs.attr({
		label: 'attrs',
		info: 'Set the attributes to keep',
		before: _ => oldinput.remove(),
		after: _ => main.submit('clip-proximity'),
		payload
	});
};
