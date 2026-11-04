(async () => {
  try {
    const info = await call("init");
    if (info.version !== "dev") throw Error("init response");
    const view = await call("view");
    if (view.running !== false) throw Error("view response");
    await call("setAuth", false);
    const settings = await call("settings");
    if (settings.auth !== false) throw Error("settings response");
    const licenses = await call("licenses");
    if (typeof licenses !== "string" || licenses.length < 100) throw Error("licenses response");
    let rejected = false;
    try { await call("smoke-unknown"); } catch (e) { rejected = e.message.includes("unknown call"); }
    if (!rejected) throw Error("error response");
    await call("start");
    if (!(await call("view")).running) throw Error("start response");
    window.passageSmokeResult = "ok";
  } catch (e) { window.passageSmokeResult = String(e); }
})();
