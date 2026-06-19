class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.1.3"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.3/reminal_0.1.3_darwin_arm64.tar.gz"
      sha256 "51f185866050127d9838a1e0dfa1a9768304dbcbd749b212f5fafa2ba4b11d32"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.3/reminal_0.1.3_darwin_amd64.tar.gz"
      sha256 "05d06d4ee85f6cdfbd0c6cddd0e23e5c906eec23b1180e8f1955e74701ee9e16"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.3/reminal_0.1.3_linux_arm64.tar.gz"
      sha256 "47cf59ade8c86e3c9520e1275d9b601651aa4f8d87d12ef221cbe8b669fb1da6"
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
