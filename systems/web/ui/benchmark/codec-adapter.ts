import { ContentWorker } from "../src/infrastructure/content-worker";

export const codec = new ContentWorker();
export const workerTimings: { op: string; roundTrip: number; worker: number }[] = [];
codec.onTiming = (sample) => {
  workerTimings.push(sample);
};
