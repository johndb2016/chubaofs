import gql from 'graphql-tag' // 引入graphql
const baseGql = {
  versionCheckList: gql`query VersionCheck {
    VersionCheck{
      iP
      message
      versionInfo{
        branchName
        commitID
        model
        version
      }
    }
  }`
}

export default baseGql
