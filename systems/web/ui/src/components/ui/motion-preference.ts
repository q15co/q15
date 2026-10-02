import { createContext, useContext } from "react";
export const ReducedMotion = createContext(true);
export const useMotionPreference = () => useContext(ReducedMotion);
