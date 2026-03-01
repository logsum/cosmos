function readFile(input) {
  return { content: fs.read(input.path) };
}

function writeFile(input) {
  fs.write(input.path, input.content);
  return { ok: true };
}

function listDir(input) {
  return { entries: fs.list(input.path) };
}

function statFile(input) {
  return fs.stat(input.path);
}

function deleteFile(input) {
  fs.unlink(input.path);
  return { ok: true };
}
