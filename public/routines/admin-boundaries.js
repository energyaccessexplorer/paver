import '../lib/selectlist.js';

import * as inputs from '../inputs.js';

import * as main from './main.js';

const header = "Adminitrative Boundaries";

const payload = {
	geographyid: null,
	dataseturl: null,
	field: 'OBJECTID'
};

main.setup({ header, payload });

inputs.geographies({
	after: x => datasetinput(x),
	payload
});

function datasetinput(oldinput) {
	return inputs.url({
		label: 'dataseturl',
		info: 'What dataset are we working with? (SHP/GEOJSON)',
		before: _ => oldinput.remove(),
		after: _ => main.submit('admin-boundaries'),
		payload
	});
};
