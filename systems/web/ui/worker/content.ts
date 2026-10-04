import { contentProcessor } from "../src/infrastructure/content-processor";

const receive = contentProcessor((value) => self.postMessage(value, []));
self.addEventListener("message", (event: MessageEvent<unknown>) => receive(event.data));
