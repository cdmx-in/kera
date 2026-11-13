import type { FileRef } from '@/api/types'

/** The {type, id} the completion request carries for an attachment: a knowledge collection stays one, everything else is a file. */
export const fileAttach = (f: Pick<FileRef, 'id' | 'type'>) => ({ type: f.type === 'collection' ? 'collection' : 'file', id: f.id })
