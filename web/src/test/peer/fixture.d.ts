import type { Codec, Identity } from "../../transport/peer/wire";
export function fixture(id: string, role: number): Promise<Identity>;
export function fixtureCodec(): Promise<Codec>;
