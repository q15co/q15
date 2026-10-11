import { parseFrameValue } from "../src/domain/protocol";
import { mainCodec } from "./base-codec";

export const codec = mainCodec(parseFrameValue);
