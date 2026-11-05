(async () => {
  try {
    await initialized;
    const info = await call("init");
    if (info.version === "9.0.2") {
      const settings = await call("settings");
      const view = await call("view");
      if (settings.auto_update || !settings.auth || view.running || view.port !== 24567 || view.user !== "keep" || view.pass !== "keep") throw Error("settings changed");
      window.passageUpdateResult = "ok";
      return;
    }
    if (info.version !== "9.0.1") throw Error("unexpected version");
    const waitState = async state => {
      for (let n = 0; n < 1200; n++) {
        const u = (await call("view")).update;
        if (u.state === state) return u;
        if (u.state === "error") throw Error(u.error);
        await new Promise(resolve => setTimeout(resolve, 100));
      }
      throw Error("update timed out: " + state);
    };
    await call("updateCheck");
    const offer = await waitState("available");
    if (offer.version !== "9.0.2") throw Error("unexpected offer");
    await call("updateDownload");
    const ready = await waitState("ready");
    if (ready.done !== ready.total || !ready.ready) throw Error("download incomplete");
    await call("updateInstall");
  } catch (e) { window.passageUpdateResult = String(e); }
})();
