import EditorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker";
import JsonWorker from "monaco-editor/esm/vs/language/json/json.worker?worker";
import YamlWorker from "./yaml.worker?worker";

export function getMonacoWorker(label: string): Worker {
  if (label === "json") return new JsonWorker();
  if (label === "yaml") return new YamlWorker();
  return new EditorWorker();
}
