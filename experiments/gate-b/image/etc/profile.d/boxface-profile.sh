case ":$PATH:" in
  *:/opt/boxface/profile/bin:*) ;;
  *) PATH="/opt/boxface/profile/bin:$PATH" ;;
esac
export PATH
