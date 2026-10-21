// The functions this renderer implements itself, instead of web_core's.
import { A2uiExpressionError, OpenUrlApi, createFunctionImplementation } from "@a2ui/web_core/v1_0";

// openUrl opens http and https addresses in a new tab with no opener and no
// referrer, and nothing else: web_core's own also lets mailto: and tel: through,
// which would start another program from a model's text. The catalog marks the
// function as needing a user gesture; web_core refuses to run it from anything
// but an action the user triggered.
export const openUrl = createFunctionImplementation(OpenUrlApi, (args) => {
  const target = typeof args.url === "string" ? args.url : "";
  let url: URL;
  try {
    url = new URL(target, window.location.href);
  } catch (cause) {
    throw new A2uiExpressionError(`Invalid URL specified: ${target}`, "openUrl", cause);
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw new A2uiExpressionError(`Unsupported URL scheme: ${url.protocol}`, "openUrl");
  }
  window.open(url.href, "_blank", "noopener,noreferrer");
});
