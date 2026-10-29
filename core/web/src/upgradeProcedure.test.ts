import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { resolve, join } from "node:path";
import { expect, it } from "vitest";
import { upgradeCommands } from "./Upgrade";

it("selects verified public tag source from branch/detached installs and stops on dirty or failed steps", () => {
  const base = mkdtempSync(resolve("../../.upgrade-fixture-"));
  const git = (cwd: string, ...args: string[]) => execFileSync("git", args, {cwd, stdio: "pipe", env: {...process.env, GIT_AUTHOR_NAME:"Test", GIT_AUTHOR_EMAIL:"test@example.com", GIT_COMMITTER_NAME:"Test", GIT_COMMITTER_EMAIL:"test@example.com"}}).toString().trim();
  try {
    const upstream = join(base, "upstream"); mkdirSync(upstream);
    git(upstream,"init","-q");
    writeFileSync(join(upstream,"VERSION"),"0.5.1\n");
    for (const script of ["install/desktop/blerg-up.sh","install/desktop/daemon/install.sh","install/k8s/deploy.sh"]) {
      mkdirSync(resolve(upstream,script,".."),{recursive:true});
      writeFileSync(join(upstream,script),'#!/bin/bash\nset -eu\n[ "$(cat ../../VERSION)" = "0.5.1" ]\n[ "$(git rev-parse HEAD)" = "$EXPECTED_REVISION" ]\necho "$PWD" >> "$BUILD_LOG"\n[ "${FAIL_BUILD:-0}" = 0 ]\n',{mode:0o755});
    }
    git(upstream,"add",".");git(upstream,"commit","-qm","release");git(upstream,"tag","v0.5.1");
    const revision=git(upstream,"rev-parse","HEAD");
    writeFileSync(join(upstream,"VERSION"),"0.6.0\n");git(upstream,"commit","-qam","unreleased");
    for (const type of ["desktop","kubernetes"]) for (const state of ["branch","detached","dirty","fetch-failure","build-failure"]) {
      const source=join(base,`${type}-${state}`);git(base,"clone","-q",upstream,source);
      if(state==="detached") git(source,"checkout","--detach","v0.5.1");
      if(state==="dirty") writeFileSync(join(source,"VERSION"),"dirty\n");
      const dir=type==="desktop"?"desktop":"k8s";
      writeFileSync(join(source,".git/info/exclude"),"install/*/.env\n");
      writeFileSync(join(source,`install/${dir}/.env`),"TAG=old\n");
      const log=join(base,`${type}-${state}.log`);
      const command=upgradeCommands(type,"0.5.1")[0];
      expect(command).toBeDefined();
      const result=spawnSync("bash",["-c",command],{cwd:source,encoding:"utf8",env:{...process.env,
        GIT_CONFIG_COUNT:"1",GIT_CONFIG_KEY_0:`url.${state==="fetch-failure"?join(base,"missing"):upstream}.insteadOf`,GIT_CONFIG_VALUE_0:"https://github.com/blerglab/blerg-ai.git",
        EXPECTED_REVISION:revision,BUILD_LOG:log,FAIL_BUILD:state==="build-failure"?"1":"0"}});
      if (["dirty","fetch-failure","build-failure"].includes(state)) {
        expect(result.status, result.stderr).not.toBe(0);
        if(state==="build-failure") expect(readFileSync(log,"utf8").trim().split("\n")).toHaveLength(1);
        else expect(()=>readFileSync(log)).toThrow();
      } else {
        expect(result.status,result.stderr).toBe(0);
        const paths=readFileSync(log,"utf8").trim().split("\n");
        expect(paths).toHaveLength(type==="desktop"?2:1);
        expect(paths.every(p=>p.includes("blerg-release-v0.5.1."))).toBe(true);
      }
      expect(readFileSync(join(source,`install/${dir}/.env`),"utf8")).toBe("TAG=old\n");
    }
  } finally {rmSync(base,{recursive:true,force:true});}
},30000);
