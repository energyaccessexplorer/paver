const origin = "https://api.energyaccessexplorer.org";
const storage = "https://wri-public-data.s3.amazonaws.com/EnergyAccess/";

const form = document.querySelector('#not-a-form-form');
const instructions = document.querySelector('#instructions');
const infopre = document.querySelector('pre');

export async function geographies({after, payload}) {
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

	form.append(sl.input);

	sl.input.addEventListener('change', function(e) {
		const geo = geos.find(x => x['cca3'] === this.value);

		payload['geographyid'] = geo['id'];
		payload['referenceurl'] = geo['boundary']['endpoint'];

		if (typeof after === 'function') after(this);
	});

	sl.input.focus();

	instructions.innerText = "Pick a geography";

	infopre.innerText = "If a geography is not on the list, it probably means it does not have a boundary_file set";
};

export async function datasetid({before, after, payload}) {
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

	if (typeof before === 'function') before();
	form.prepend(sl.input);

	sl.input.addEventListener('change', function(e) {
		payload['datasetid'] = this.value;
		if (typeof after === 'function') after(this);
	});

	sl.input.focus();

	instructions.innerText = "Pick a dataset";

	infopre.innerText = "This will one day automatically relate the generated files with this dataset...";
};

export async function url({label = "<unset>", info = "", before, after, payload}) {
	const input = document.createElement('input');
	input.setAttribute('required', '');
	input.setAttribute('type', 'url');
	input.setAttribute('name', 'location');
	input.setAttribute('autocomplete', 'off');

	input.value = storage;

	if (typeof before === 'function') before();
	form.prepend(input);

	input.focus();

	input.addEventListener('change', async function(e) {
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

	instructions.innerText = "Give a URL go to get the file";

	infopre.innerText = info;
};
