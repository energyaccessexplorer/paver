import * as socket from '../socket.js';

import * as inputs from '../inputs.js';

const loading = document.querySelector('tb-loading');
const infopre = document.querySelector('pre');

let _payload;

export async function submit(r) {
	var body = [];
	for (var p in _payload)
		body.push(encodeURIComponent(p) +
							"=" +
							encodeURIComponent(_payload[p]));


	loading.style.display = 'block';
	infopre.innerText = "";

	socket.listen(m => infopre.innerText += "\n" + m);

	const response = await fetch(`/routines?routine=${r}`, {
		method: 'POST',
		headers: {
			'Content-Type': 'application/x-www-form-urlencoded'
		},
		body: body.join("&"),
	}).then(async r => {
		if (!r.ok) {
			const msg = await r.text();
			infopre.innerText = `
${r.status} - ${r.statusText}

${msg}`;
		}
		});

	loading.style.display = '';
};

export function setup({header, payload}) {
	const headerel = document.querySelector('header');
	headerel.innerText = header;

	_payload = payload;
};

export async function info(response) {
	const msg = await response.text();

	if (!response.ok) {
		infopre.innerText = `
${response.status} - ${response.statusText}

${msg}`;
	}
};
