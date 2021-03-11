const origin = "https://api.energyaccessexplorer.org";
const storage = "https://wri-public-data.s3.amazonaws.com/EnergyAccess/";

const form = document.querySelector('#not-a-form-form');
const infopre = document.querySelector('pre');

export async function geographies({before, after, payload}) {
	if (typeof before === 'function') before();

	const path = "/geographies?select=id,name,cca3,boundary(id,endpoint)&boundary_file=not.is.null";
	const geos = await fetch(origin + path)
				.then(r => r.json());

	const sl = new selectlist(
		"geographies",
		geos.reduce((a,c) => {
		  a[c['cca3']] = c['name'];
		  return a;
		}, {})
	);

	sl.input.setAttribute('required', true);
	sl.input.setAttribute('placeholder', "Select a geography");
	sl.input.addEventListener('change', function(e) {
		const geo = geos.find(x => x['cca3'] === this.value);

		payload['geographyid'] = geo['id'];
		payload['referenceurl'] = geo['boundary']['endpoint'];

		if (typeof after === 'function') after(this);
	});

	form.append(sl.input);

	sl.input.focus();

	infopre.innerText = "If a geography is not on the list, it probably means it does not have a boundary_file set";
};

export async function datasetid({before, after, payload}) {
	if (typeof before === 'function') before();

	const path = `/datasets?select=id,name,category_name&geography_id=eq.${payload['geographyid']}`;
	const datas = await fetch(origin + path)
				.then(r => r.json());

	const sl = new selectlist(
		"datasets",
		datas.reduce((a,c) => {
		  a[c['id']] = (c['name'] ? c['name'] : c['category_name']);
		  return a;
		}, {})
	);

	sl.input.setAttribute('required', true);
	sl.input.setAttribute('placeholder', "Select a dataset");
	sl.input.addEventListener('change', function(e) {
		payload['datasetid'] = this.value;
		if (typeof after === 'function') after(this);
	});

	form.prepend(sl.input);

	sl.input.focus();

	infopre.innerText = "This will one day automatically relate the generated files with this dataset...";
};

export function url({label = "<unset>", before, after, payload}) {
	if (typeof before === 'function') before();

	const input = document.createElement('input');
	input.setAttribute('required', true);
	input.setAttribute('type', 'url');
	input.setAttribute('name', 'location');
	input.setAttribute('autocomplete', 'off');
	input.setAttribute('placeholder', "URL for target dataset");
	input.setAttribute('pattern', 'http(.*).geojson');
	input.addEventListener('change', async function(e) {
		if (!input.checkValidity()) {
			input.reportValidity();
			return;
		}

		payload[label] = this.value;

		if (typeof after === 'function') after(input);

		return;

		const response = await fetch(this.value, {
		  method: "HEAD"
		}).catch(err => {
		  infopre.innerText = err + "\n(probably a CORS error, check the console log in the developer tools)";
		}).then(async r => {
			if (!r) return;

			if (r.ok) {
				payload[label] = this.value;
				if (typeof after === 'function') after(input);
			}
			else {
				const msg = await r.text();
				infopre.innerText = `
${r.status} - ${r.statusText}

${msg}`;
}
		});
	});
	input.value = storage;

	form.prepend(input);

	input.focus();

	infopre.innerText = "URL to *.geojson file. The original file will not be modified.";
};

export function attr({before, after, payload}) {
	if (typeof before === 'function') before();

	const input = document.createElement('input');

	input.setAttribute('required', true);
	input.setAttribute('placeholder', "Dataset relevant fields");
	input.setAttribute('pattern', "^[a-zA-Z0-9]+(,[a-zA-Z0-9]+)*$")
	input.addEventListener('change', function(e) {
		if (!input.checkValidity()) {
			input.reportValidity();
			return;
		}

		payload['attrs'] = this.value.split(',').map(x => x.trim()).join(',');

		if (typeof after === 'function') after(this);
	});

	form.prepend(input);

	input.focus();

	infopre.innerText = `
It should be a comma-separated list - no spaces. The other fields will be discarded.

two <= correct
uno,two,4 <= correct
uno, two,4,, <= triply incorrect
`;
};
