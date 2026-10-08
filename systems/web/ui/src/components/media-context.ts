import { createContext } from "react";

import type { Media } from "../application/ports";

export const MediaContext = createContext<Media | undefined>(undefined);
