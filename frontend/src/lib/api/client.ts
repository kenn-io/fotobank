import * as operations from "./generated/client";

export type Client = typeof operations;
export const api: Client = operations;
