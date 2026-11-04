(async () => {
  try {
    await initialized;
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
    page("settings");
    await new Promise(resolve => setTimeout(resolve, 100));
    $("my").value = "unsaved route";
    window.dispatchEvent(new Event("focus"));
    await new Promise(resolve => setTimeout(resolve, 100));
    if ($("my").value !== "unsaved route") throw Error("focus discarded a draft");
    page("home");
    const waitFor = async test => {
      for (let n = 0; n < 100; n++) {
        if (test()) return;
        await new Promise(resolve => setTimeout(resolve, 50));
      }
      throw Error("URL import timed out");
    };
    await waitFor(() => !$("modal").classList.contains("hidden"));
    if ((await call("link", false)).has) throw Error("URL saved without confirmation");
    $("modalCancel").click();
    await incomingImports;
    if ((await call("link", false)).has) throw Error("URL saved after cancel");
    window.passageSmokeResult = "send-url";
    await waitFor(() => !$("modal").classList.contains("hidden"));
    if ((await call("link", false)).has) throw Error("warm URL saved without confirmation");
    $("modalOk").click();
    await incomingImports;
    if (!(await call("link", false)).has) throw Error("URL was not imported");
    await call("saveLink", "");
    await call("start");
    if (!(await call("view")).running) throw Error("start response");
    window.passageSmokeResult = "ok";
  } catch (e) { window.passageSmokeResult = String(e); }
})();
