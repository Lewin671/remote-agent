async page => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.addInitScript(() => {
    const perf = {
      t0: performance.now(),
      longTasks: [],
      paints: [],
      cls: 0,
      wasmCalls: [],
      frames: [],
      framePhase: 'cold-load',
      recordingFrames: true,
    };
    window.__perfData = perf;

    try {
      new PerformanceObserver((list) => {
        for (const e of list.getEntries()) {
          perf.longTasks.push({
            start: Math.round(e.startTime * 10) / 10,
            duration: Math.round(e.duration * 10) / 10,
            phase: perf.framePhase,
          });
        }
      }).observe({ type: 'longtask', buffered: true });
    } catch {}

    try {
      new PerformanceObserver((list) => {
        for (const e of list.getEntries()) {
          perf.paints.push({ name: e.name, start: Math.round(e.startTime * 10) / 10 });
        }
      }).observe({ type: 'paint', buffered: true });
    } catch {}

    try {
      new PerformanceObserver((list) => {
        for (const e of list.getEntries()) {
          if (!e.hadRecentInput) perf.cls += e.value;
        }
      }).observe({ type: 'layout-shift', buffered: true });
    } catch {}

    let lastRaf = performance.now();
    function rafTick(now) {
      if (perf.recordingFrames) {
        const dt = now - lastRaf;
        perf.frames.push({ t: Math.round(now), dt: Math.round(dt * 10) / 10, phase: perf.framePhase });
      }
      lastRaf = now;
      requestAnimationFrame(rafTick);
    }
    requestAnimationFrame(rafTick);

    let wrappedCore;
    function wrapCore(raw) {
      if (!raw || typeof raw !== 'object') return raw;
      const wrapped = Object.assign({}, raw);
      for (const fn of ['dispatch', 'view']) {
        if (typeof raw[fn] === 'function') {
          wrapped[fn] = function (...args) {
            const tStart = performance.now();
            const res = raw[fn].apply(this, args);
            const dt = performance.now() - tStart;
            const inLen = args[1] && args[1].byteLength ? args[1].byteLength : (args[0] && args[0].byteLength ? args[0].byteLength : 0);
            const outLen = res && res.data && res.data.byteLength ? res.data.byteLength : 0;
            if (perf.wasmCalls.length < 15000) {
              perf.wasmCalls.push({
                fn,
                t: Math.round(tStart * 10) / 10,
                dt: Math.round(dt * 100) / 100,
                inBytes: inLen,
                outBytes: outLen,
                phase: perf.framePhase,
              });
            }
            return res;
          };
        }
      }
      return wrapped;
    }

    try {
      Object.defineProperty(globalThis, '__remoteAgentCore', {
        configurable: true,
        enumerable: true,
        get() { return wrappedCore; },
        set(v) { wrappedCore = wrapCore(v); },
      });
    } catch {}
  });

  const navStart = Date.now();
  await page.reload({ waitUntil: 'domcontentloaded' });
  const domLoadedMs = Date.now() - navStart;

  await page.waitForSelector('.body.chat', { timeout: 30000 });
  const chatMountMs = Date.now() - navStart;

  await page.waitForFunction(() => {
    const el = document.querySelector('.body.chat');
    const rows = document.querySelectorAll('[data-item-index]');
    return el && !el.classList.contains('mo-hold') && rows.length > 0;
  }, { timeout: 60000 });
  const interactiveMs = Date.now() - navStart;
  await page.waitForTimeout(400);

  const coldSnap = await page.evaluate(() => {
    const el = document.querySelector('.body.chat');
    const rows = [...document.querySelectorAll('[data-item-index]')];
    return {
      domNodes: document.querySelectorAll('*').length,
      scrollHeight: el ? el.scrollHeight : 0,
      clientHeight: el ? el.clientHeight : 0,
      scrollTop: el ? el.scrollTop : 0,
      renderedRows: rows.length,
      firstIdx: rows[0]?.getAttribute('data-item-index'),
      lastIdx: rows[rows.length - 1]?.getAttribute('data-item-index'),
      rect: el ? el.getBoundingClientRect().toJSON() : null,
    };
  });

  const cx = Math.round(coldSnap.rect.left + coldSnap.rect.width / 2);
  const cy = Math.round(coldSnap.rect.top + coldSnap.rect.height / 2);
  await page.mouse.move(cx, cy);

  await page.evaluate(() => { window.__perfData.framePhase = 'scroll-up'; });
  let loadEarlierRounds = 0;
  for (let round = 0; round < 15; round++) {
    for (let i = 0; i < 30; i++) {
      await page.mouse.wheel(0, -1200);
      await page.waitForTimeout(25);
      const top = await page.evaluate(() => document.querySelector('.body.chat')?.scrollTop ?? 0);
      if (top <= 20) break;
    }
    await page.waitForFunction(
      () => !document.querySelector('.load-earlier .muted'),
      { timeout: 20000 },
    ).catch(() => {});
    await page.waitForTimeout(300);
    const st = await page.evaluate(() => {
      const el = document.querySelector('.body.chat');
      const le = document.querySelector('.load-earlier');
      return {
        scrollTop: el?.scrollTop ?? 0,
        scrollHeight: el?.scrollHeight ?? 0,
        hasLoadEarlier: !!le,
      };
    });
    if (st.hasLoadEarlier) loadEarlierRounds++;
    if (st.scrollTop <= 20 && !st.hasLoadEarlier) break;
  }

  const topSnap = await page.evaluate(() => {
    const el = document.querySelector('.body.chat');
    const rows = [...document.querySelectorAll('[data-item-index]')];
    return {
      domNodes: document.querySelectorAll('*').length,
      scrollHeight: el?.scrollHeight ?? 0,
      scrollTop: el?.scrollTop ?? 0,
      renderedRows: rows.length,
      firstIdx: rows[0]?.getAttribute('data-item-index'),
      lastIdx: rows[rows.length - 1]?.getAttribute('data-item-index'),
      hasLoadEarlier: !!document.querySelector('.load-earlier'),
    };
  });

  await page.evaluate(() => { window.__perfData.framePhase = 'scroll-down'; });
  for (let i = 0; i < 200; i++) {
    await page.mouse.wheel(0, 1400);
    await page.waitForTimeout(25);
    const gap = await page.evaluate(() => {
      const el = document.querySelector('.body.chat');
      return el ? el.scrollHeight - el.scrollTop - el.clientHeight : 0;
    });
    if (gap <= 10) break;
  }
  await page.waitForTimeout(300);

  const finalMetrics = await page.evaluate(() => {
    const perf = window.__perfData;
    perf.recordingFrames = false;

    function summarizeFrames(phase) {
      const fs = perf.frames.filter(f => f.phase === phase).map(f => f.dt);
      if (!fs.length) return { count: 0, avgDt: 0, p95Dt: 0, maxDt: 0, over20ms: 0, over33ms: 0, over50ms: 0 };
      const sorted = [...fs].sort((a, b) => a - b);
      return {
        count: fs.length,
        avgDt: Math.round((fs.reduce((a, b) => a + b, 0) / fs.length) * 10) / 10,
        p95Dt: sorted[Math.floor(sorted.length * 0.95)],
        maxDt: sorted[sorted.length - 1],
        over20ms: fs.filter(x => x > 20).length,
        over33ms: fs.filter(x => x > 33.4).length,
        over50ms: fs.filter(x => x > 50).length,
      };
    }

    function summarizeWasm(phase, fn) {
      const calls = perf.wasmCalls.filter(c => c.phase === phase && c.fn === fn);
      const totalMs = calls.reduce((a, b) => a + b.dt, 0);
      const maxMs = calls.reduce((m, b) => Math.max(m, b.dt), 0);
      const totalOutBytes = calls.reduce((a, b) => a + b.outBytes, 0);
      const maxOutBytes = calls.reduce((m, b) => Math.max(m, b.outBytes), 0);
      const totalInBytes = calls.reduce((a, b) => a + b.inBytes, 0);
      const sessViews = fn === 'view' ? calls.filter(c => c.inBytes >= 22) : [];
      return {
        count: calls.length,
        totalMs: Math.round(totalMs * 100) / 100,
        maxMs: Math.round(maxMs * 100) / 100,
        totalInBytes,
        totalOutBytes,
        maxOutBytes,
        sessionViewCount: sessViews.length,
        sessionViewTotalMs: Math.round(sessViews.reduce((a, b) => a + b.dt, 0) * 100) / 100,
        sessionViewMaxMs: Math.round(sessViews.reduce((m, b) => Math.max(m, b.dt), 0) * 100) / 100,
        sessionViewMaxOutBytes: sessViews.reduce((m, b) => Math.max(m, b.outBytes), 0),
      };
    }

    return {
      paints: perf.paints,
      cls: Math.round(perf.cls * 10000) / 10000,
      longTasks: perf.longTasks,
      longTaskCount: perf.longTasks.length,
      longTaskTotalMs: Math.round(perf.longTasks.reduce((a, b) => a + b.duration, 0) * 10) / 10,
      frames: {
        coldLoad: summarizeFrames('cold-load'),
        scrollUp: summarizeFrames('scroll-up'),
        scrollDown: summarizeFrames('scroll-down'),
      },
      wasm: {
        coldDispatch: summarizeWasm('cold-load', 'dispatch'),
        coldView: summarizeWasm('cold-load', 'view'),
        scrollUpDispatch: summarizeWasm('scroll-up', 'dispatch'),
        scrollUpView: summarizeWasm('scroll-up', 'view'),
        scrollDownDispatch: summarizeWasm('scroll-down', 'dispatch'),
        scrollDownView: summarizeWasm('scroll-down', 'view'),
      },
      finalDomNodes: document.querySelectorAll('*').length,
      finalScrollHeight: document.querySelector('.body.chat')?.scrollHeight ?? 0,
    };
  });

  return {
    domLoadedMs,
    chatMountMs,
    interactiveMs,
    coldSnap,
    loadEarlierRounds,
    topSnap,
    finalMetrics,
  };
}
