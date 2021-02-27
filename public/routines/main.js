import * as socket from '../socket.js';

import * as inputs from '../inputs.js';

let payload;
window.payload = payload;

export async function submit(r) {
	var body = [];

	const loading = document.querySelector('tb-loading');
	const infopre = document.querySelector('pre');

	for (var p in payload)
		body.push(encodeURIComponent(p) +
							"=" +
							encodeURIComponent(payload[p]));


	loading.style.display = 'block';
	infopre.innerText = "";

	socket.listen(m => infopre.innerText += "\n" + m);

	const response = await fetch(`/routines?routine=${r}`, {
		method: 'POST',
		headers: {
			'Content-Type': 'application/x-www-form-urlencoded'
		},
		body: body.join("&"),
	});

	inputs.infoerror(response);

	loading.style.display = '';
};

export function setup({header, payload}) {
	const headerel = document.querySelector('header');
	headerel.innerText = header;

	payload = payload;
};
