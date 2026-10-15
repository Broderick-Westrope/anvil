"use strict";
(function (global) {
  class BashError extends Error {
    constructor(message) { super(message); this.name = "BashError"; }
  }
  class SchemaError extends Error {
    constructor(message) { super(message); this.name = "SchemaError"; }
  }
  class Cancelled extends Error {
    constructor(message) { super(message); this.name = "Cancelled"; }
  }
  class JobsRunning extends Error {
    constructor(message) { super(message); this.name = "JobsRunning"; }
  }
  const errorTypes = { BashError, SchemaError, Cancelled, JobsRunning };
  const native = global.__native;
  delete global.__native;

  function invoke(name, args) {
    const env = JSON.parse(native(name, JSON.stringify(args === undefined ? {} : args)));
    if (env.ok) return env.value;
    const Type = errorTypes[env.error.name] || Error;
    const err = new Type(env.error.message);
    if (env.error.fields) Object.assign(err, env.error.fields);
    throw err;
  }

  function handle(id) {
    return Object.freeze({
      id,
      turn(prompt, opts) {
        return invoke("turn", Object.assign({}, opts, { session: id, prompt }));
      },
      fork(opts) {
        return handle(invoke("session.fork", Object.assign({}, opts, { session: id })));
      },
    });
  }

  const now = () => invoke("now");
  const random = () => invoke("random");

  Object.assign(global, {
    BashError, SchemaError, Cancelled, JobsRunning,
    requires() {},
    now, random,
    bash: (cmd, opts) => invoke("bash", Object.assign({}, opts, { cmd })),
    agent: (name, prompt, opts) => invoke("agent", Object.assign({}, opts, { agent: name, prompt })),
    parallel: (calls) => invoke("parallel", { calls }),
    skill: (name) => invoke("skill", { name }),
    reference: (path) => invoke("reference", { path }),
    readFile: (path) => invoke("readFile", { path }),
    ask: (question, opts) => invoke("ask", Object.assign({}, opts, { question })),
    status: (step, detail) => {
      invoke("status", { step: String(step), detail: detail === undefined ? "" : String(detail) });
    },
    session: Object.freeze({
      current: () => handle(invoke("session.current")),
      new: (opts) => handle(invoke("session.new", opts || {})),
    }),
    workflow: Object.freeze({
      tmpdir: () => invoke("workflow.tmpdir"),
      dir: () => invoke("workflow.dir"),
      exclusive: (fn, opts) => {
        invoke("workflow.hold", { stopJobs: !!(opts && opts.stopJobs) });
        try {
          return fn();
        } finally {
          invoke("workflow.release");
        }
      },
    }),
  });

  // Date and Math.random would make replay diverge, so they go through the
  // recorded primitives.
  Math.random = random;
  const RealDate = global.Date;
  RealDate.now = now;
  global.Date = new Proxy(RealDate, {
    construct(target, args, newTarget) {
      if (args.length === 0) {
        throw new Error("new Date() without arguments isn't replay-safe; use new Date(now())");
      }
      return Reflect.construct(target, args, newTarget);
    },
    apply() {
      throw new Error("Date() isn't replay-safe; use new Date(now())");
    },
  });
})(globalThis);
