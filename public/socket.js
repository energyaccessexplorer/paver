export function listen(fn) {
	const c = new WebSocket(`ws://${location.host}/socket`)

	c.addEventListener("open", _ => console.info("connected!"));

	c.addEventListener("close", e => console.log(`WebSocket Disconnected code: ${e.code}, reason: ${e.reason}`, e));

	c.addEventListener("message", e => {
		if (typeof fn === 'function') fn(e.data);
		console.log(e.data);
	});
};
