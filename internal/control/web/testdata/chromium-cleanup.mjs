import {rm} from 'node:fs/promises';

export function chromiumExit(child) {
  return new Promise(resolve => {
    child.once('exit', (code, signal) => resolve({code, signal}));
    child.once('error', error => resolve({error}));
  });
}

export async function removeChromiumProfile(child, exited, profile, graceful = false) {
  const wait = async ms => {
    let timer;
    try {
      return await Promise.race([exited, new Promise(resolve => { timer = setTimeout(resolve, ms); })]);
    } finally {
      clearTimeout(timer);
    }
  };
  if (!graceful || !await wait(3000)) child.kill();
  if (!await wait(3000)) {
    child.kill('SIGKILL');
    if (!await wait(3000)) throw new Error('Chromium did not exit');
  }
  // On Windows, Chromium subprocesses can briefly retain profile files after exit.
  await rm(profile, {recursive: true, force: true, maxRetries: 8, retryDelay: 150});
}
