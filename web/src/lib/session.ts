import { createContext, useContext } from 'react'

// Fail closed until the server has identified this connection.
export const ReadOnlyContext = createContext(true)
export const useReadOnly = () => useContext(ReadOnlyContext)
