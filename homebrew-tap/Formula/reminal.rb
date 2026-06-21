class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.1"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.1/reminal_0.3.1_darwin_arm64.tar.gz"
      sha256 "b8857317ba50da03c52a34d4c85e6d0588cf0a6d9e89e23d3e3bf9dc2bcddcb3"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.1/reminal_0.3.1_darwin_amd64.tar.gz"
      sha256 "841b9e7b67f4ce72419f5959ea8dd02056cca36f28c8f339b60f123c85a8ad13"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.1/reminal_0.3.1_linux_arm64.tar.gz"
      sha256 "c5cfee06d6d5fb2cbe743faefdd911779f51f9bb47b1e3c04166c8cff543736c"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
